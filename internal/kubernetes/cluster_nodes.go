// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MigrateTimeout                = 6000
	RedisCommandMigrate           = "migrate"
	RedisKeysOption               = "KEYS"
	RedisReplaceOption            = "REPLACE"
	RedisClusterCommand           = "CLUSTER"
	RedisAskRedirectionResponse   = "ASK"
	RedisDeleleCommand            = "DEL"
	RedisBusyKeyResponse          = "BUSYKEY"
	RedisSetSlotStableSubCommand  = "STABLE"
	RedisSetSlotSubCommand        = "setslot"
	RedisNodeSubCommand           = "node"
	RedisImportingState           = "importing"
	RedisMigratingState           = "migrating"
	RedisClusterDownResponse      = "CLUSTERDOWN"
	RedisClusterTotalSlots        = 16384
	RedisNodesUnbalancedThreshold = 2
)

var ErrAllWeightsZero = errors.New("all nodes weights are set to 0: Impossible to calculate Slot movements")

type ClusterNode struct {
	*corev1.Pod
	*redis.ClusterNode
}

// MoveMapOptions holds the options for calculating slot move map.
type MoveMapOptions struct {
	threshold int
	weights   map[string]int
}

type MoveSequence struct {
	From     string
	FromNode *ClusterNode
	To       string
	ToNode   *ClusterNode
	Slots    []int
}

type ClusterNodeList struct {
	client ctrlClient.Client
	Nodes  []*ClusterNode
}

type MoveSlotOption struct {
	PurgeKeys bool
}

func (clusterNode *ClusterNode) Role() string {
	if clusterNode.IsMaster() {
		return "master"
	} else {
		return "replica"
	}
}

func (clusterNode *ClusterNode) IsMaster() bool {
	return clusterNode.Info().HasFlag("master")
}

func (clusterNode *ClusterNode) IsReplica() bool {
	return !clusterNode.Info().HasFlag("master")
}

func (clusterNode *ClusterNode) String() string {
	out, _ := json.Marshal(map[string]interface{}{
		"pod":     clusterNode.Pod.Name,
		"ip":      clusterNode.Pod.Status.PodIP,
		"node_id": clusterNode.ClusterNode.Name(),
		"role":    clusterNode.Role(),
	})
	return string(out)
}

func (clusterNode *ClusterNode) GetIp() string {
	return clusterNode.Pod.Status.PodIP
}

func GetKubernetesClusterNodes(ctx context.Context, client ctrlClient.Client, redisCluster *redisv1.RedisCluster) (ClusterNodeList, error) {
	componentLabel := GetStatefulSetSelectorLabel(ctx, client, redisCluster)
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel: redisCluster.Name,
			componentLabel:          "redis",
		},
	)

	pods := &corev1.PodList{}
	err := client.List(ctx, pods, &ctrlClient.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return ClusterNodeList{}, err
	}

	nodes := make([]*ClusterNode, 0)

	// Sort the pods by their ordinals as sorted list will be necessary for calculations on scale down
	prefix := fmt.Sprintf("%s-", redisCluster.Name)

	sort.Slice(pods.Items, func(i, j int) bool {
		ordinali, _ := strconv.Atoi(strings.TrimPrefix(pods.Items[i].Name, prefix))
		ordinalj, _ := strconv.Atoi(strings.TrimPrefix(pods.Items[j].Name, prefix))
		return ordinali < ordinalj
	})
	for _, pod := range pods.Items {
		clusterNode, err := redis.NewClusterNode(fmt.Sprintf("%s:%s", pod.Status.PodIP, strconv.Itoa(redis.RedisCommPort)))
		if err != nil {
			return ClusterNodeList{}, err
		}
		nodes = append(nodes, &ClusterNode{
			Pod:         pod.DeepCopy(),
			ClusterNode: clusterNode,
		})
	}
	return ClusterNodeList{
		Nodes:  nodes,
		client: client,
	}, nil
}

func FreeKubernetesClusterNode(clusterNode *ClusterNode) error {
	return clusterNode.R().Close()
}

func (nodeList *ClusterNodeList) String() string {
	var out []map[string]interface{}
	for _, clusterNode := range nodeList.Nodes {
		out = append(out, map[string]interface{}{
			"pod":     clusterNode.Pod.Name,
			"ip":      clusterNode.Pod.Status.PodIP,
			"node_id": clusterNode.ClusterNode.Name(),
		})
	}
	result, _ := json.Marshal(out)
	return string(result)
}

// Returns the found node or an error if not found.
func (nodeList *ClusterNodeList) GetNodeByPodName(name string) (*ClusterNode, error) {
	if name == "" {
		return nil, fmt.Errorf("pod name is empty")
	}

	for _, node := range nodeList.Nodes {
		if node != nil && node.Pod.Name == name {
			return node, nil
		}
	}

	return nil, fmt.Errorf("node with pod name '%s' not found", name)
}

func (nodeList *ClusterNodeList) GetNodeByID(id string) (*ClusterNode, error) {
	if id == "" {
		return nil, fmt.Errorf("pod id is empty")
	}

	for _, node := range nodeList.Nodes {
		if node != nil && node.ClusterNode.Name() == id {
			return node, nil
		}
	}
	return nil, fmt.Errorf("node ID not found")
}

func (nodeList *ClusterNodeList) SimpleNodesObject() []map[string]interface{} {
	var out []map[string]interface{}
	for _, clusterNode := range nodeList.Nodes {
		out = append(out, map[string]interface{}{
			"pod":     clusterNode.Pod.Name,
			"ip":      clusterNode.Pod.Status.PodIP,
			"node_id": clusterNode.ClusterNode.Name(),
		})
	}
	return out
}

func (nodeList *ClusterNodeList) GetMasterReplicas(master *ClusterNode) ([]*ClusterNode, error) {
	var result []*ClusterNode
	replicas, err := nodeList.GetReplicas()
	if err != nil {
		return nil, err
	}
	for _, replica := range replicas {
		if replica.Replicate() == master.ClusterNode.Name() {
			result = append(result, replica)
		}
	}
	return result, nil
}

func (nodeList *ClusterNodeList) GetMasters() ([]*ClusterNode, error) {
	var result []*ClusterNode
	for _, node := range nodeList.Nodes {
		if node.HasFlag("master") {
			result = append(result, node)
		}
	}
	return result, nil
}

func (nodeList *ClusterNodeList) GetReplicas() ([]*ClusterNode, error) {
	var result []*ClusterNode
	for _, node := range nodeList.Nodes {
		err := node.LoadInfo(true)
		if err != nil {
			return nil, err
		}
		if !node.HasFlag("master") {
			result = append(result, node)
		}
	}
	return result, nil
}

func (nodeList *ClusterNodeList) LoadInfoForNodes() error {
	for _, node := range nodeList.Nodes {
		err := node.LoadInfo(true)
		if err != nil {
			return err
		}
	}
	return nil
}

func (nodeList *ClusterNodeList) ClusterMeet(ctx context.Context) error {
	if len(nodeList.Nodes) == 0 {
		return fmt.Errorf("node nodeList is empty")
	}

	for _, sourceNode := range nodeList.Nodes {
		if sourceNode == nil {
			continue // Skip nil nodes
		}

		for _, targetNode := range nodeList.Nodes {
			if targetNode == nil || sourceNode.ClusterNode.Name() == targetNode.ClusterNode.Name() {
				continue // Skip nil nodes and self-join attempts
			}

			err := sourceNode.ClusterNode.R().ClusterMeet(ctx, targetNode.Status.PodIP, strconv.Itoa(redis.RedisCommPort)).Err()
			if err != nil {
				return fmt.Errorf("error in ClusterMeet between '%s' and '%s': %w", sourceNode.Name(), targetNode.Name(), err)
			}
		}
	}

	return nil
}

func (nodeList *ClusterNodeList) EnsureClusterSlotsStable(ctx context.Context, log logr.Logger) error {

	masterNodes, err := nodeList.GetMasters()
	if err != nil {
		return fmt.Errorf("error getting master nodes: %w", err)
	}
	for _, node := range masterNodes {
		// Case 1: Slot is in migrating on one node, and importing in another, but might have keys left.
		// Let's try to move the slot.
		if err := stabilizeNodeSlots(ctx, node, nodeList); err != nil {
			return fmt.Errorf("error stabilizing node slots: %w", err)
		}
		// Case 2: Slot is in importing on one node, and not migrating in it's source node.
		// We've covered correct cases previously, so we can safely assume we have an incorrect mapping here.
		// We'll try to find the owner of the slot, and if we can't we'll assume we own it and mark it as such
		if err := fixIncorrectImporting(node, masterNodes); err != nil {
			return fmt.Errorf("error fixing incorrect importing: %w", err)
		}
		// Case 3: Slot is in importing on one node, and has no direct owner.
		// We need to see whether another node is trying to migrate the slot,
		// and if not. we assume we own it.
		if err := handleNoDirectOwner(ctx, node, masterNodes, nodeList); err != nil {
			return fmt.Errorf("error handling slots with no direct owner: %w", err)
		}
		// Case 4: Slot is in migrating on one node, but no node is marked as importing.
		// If another node alreay owns this slots, we can just mark it as stable.
		// If not already assigned, we can assume we wanted to move the slot, and just continue doing it.
		if err := slotsInMigratingButNoNodeImporting(ctx, node, masterNodes, nodeList, log); err != nil {
			return fmt.Errorf("error continuing incomplete migrations: %w", err)
		}
	}
	// We've tried to cover the most common cases in https://github.com/redis/redis/blob/unstable/src/redis-cli.c#L5012
	// If we find more cases, we can add them here as well.
	return nil
}

func stabilizeNodeSlots(ctx context.Context, node *ClusterNode, nodeList *ClusterNodeList) error {
	err := node.ClusterNode.LoadInfo(false) // Get the latest information for the node
	if err != nil {
		return fmt.Errorf("error loading info for node '%s': %w", node.Name(), err)
	}

	for slot, sourceId := range node.ClusterNode.Importing() {
		if err := setSlotStable(node, slot); err != nil {
			return fmt.Errorf("error setting slot %d as stable: %w", slot, err)
		}

		sourceNode, err := nodeList.GetNodeByID(sourceId)
		if err != nil {
			return fmt.Errorf("error getting source node by ID '%s': %w", sourceId, err)
		}

		if sourceNode.IsMigrating(slot) {
			if err := completeSlotMigration(ctx, nodeList, sourceNode, node, slot); err != nil {
				return fmt.Errorf("error completing migration for slot %d: %w", slot, err)
			}
		}

		node.RemoveImporting(slot)
		sourceNode.ClusterNode.RemoveMigrating(slot)
	}

	return nil
}

func setSlotStable(node *ClusterNode, slot int) error {
	err := node.Call(RedisClusterCommand, RedisSetSlotSubCommand, slot, RedisSetSlotStableSubCommand).Err()
	return err
}

func completeSlotMigration(ctx context.Context, nodeList *ClusterNodeList, sourceNode, destNode *ClusterNode, slot int) error {
	fmt.Printf("Fixing incomplete migration of slot %v from %v to %v\n", slot, sourceNode.Name(), destNode.Name())
	time.Sleep(2 * time.Second) // Waiting before moving the slot

	err := nodeList.MoveSlot(ctx, sourceNode, destNode, slot, MoveSlotOption{PurgeKeys: false})
	if err != nil {
		return fmt.Errorf("cannot move slot %d from %s to %s: %v", slot, sourceNode.Name(), destNode.Name(), err)
	}

	return nil
}

func fixIncorrectImporting(node *ClusterNode, masterNodes []*ClusterNode) error {
	for slot := range node.Importing() {
		ownerNode := getOwner(masterNodes, slot)
		if ownerNode != nil {
			if err := markSlotStableAndAssignOwner(ownerNode, masterNodes, slot); err != nil {
				return fmt.Errorf("error fixing incorrect importing for slot %d: %w", slot, err)
			}
			node.RemoveImporting(slot)
		}
	}

	return nil
}

func getOwner(masterNodes []*ClusterNode, slot int) *ClusterNode {
	for _, node := range masterNodes {
		if node.ClusterNode.OwnsSlot(slot) {
			return node
		}
	}
	return nil
}

func getMigratingNode(masterNodes []*ClusterNode, slot int) *ClusterNode {
	for _, node := range masterNodes {
		if node.ClusterNode.IsMigrating(slot) {
			return node
		}
	}
	return nil
}

func markSlotStableAndAssignOwner(ownerNode *ClusterNode, masterNodes []*ClusterNode, slot int) error {
	if err := setSlotStable(ownerNode, slot); err != nil {
		return err
	}

	for _, node := range masterNodes {
		if err := node.Call(RedisClusterCommand, RedisSetSlotSubCommand, slot, RedisNodeSubCommand, ownerNode.ClusterNode.Name()).Err(); err != nil {
			return err
		}
	}

	return nil
}

func handleNoDirectOwner(ctx context.Context, node *ClusterNode, masterNodes []*ClusterNode, nodeList *ClusterNodeList) error {
	for slot := range node.Importing() {
		ownerNode := getOwner(masterNodes, slot)
		if ownerNode == nil {
			migratingNode := getMigratingNode(masterNodes, slot)
			if migratingNode != nil {
				if node.ClusterNode.Name() == migratingNode.ClusterNode.Name() {
					if err := nodeList.MoveSlot(ctx, migratingNode, node, slot, MoveSlotOption{PurgeKeys: false}); err != nil {
						return err
					}
					node.RemoveImporting(slot)
					migratingNode.ClusterNode.RemoveMigrating(slot)
				}
			} else {
				if err := markSlotAsOwned(node, slot); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func markSlotAsOwned(node *ClusterNode, slot int) error {
	if err := setSlotStable(node, slot); err != nil {
		return err
	}
	if err := node.Call(RedisClusterCommand, RedisSetSlotSubCommand, slot, RedisNodeSubCommand, node.Name()).Err(); err != nil {
		return err
	}
	node.RemoveImporting(slot)
	return nil
}

func slotsInMigratingButNoNodeImporting(ctx context.Context, node *ClusterNode, masterNodes []*ClusterNode, nodeList *ClusterNodeList, log logr.Logger) error {
	for slot, destination := range node.Migrating() {
		destNode, err := nodeList.GetNodeByID(destination)
		if err != nil {
			log.Info("Could not find destination node, skipping migration", "slot", slot, "destination", destination)
			continue
		}

		// If another master node already owns this slot, set it as stable
		ownerNode := getOwner(masterNodes, slot)
		if ownerNode != nil && node.Name() != ownerNode.Name() {
			if err := setSlotStable(node, slot); err != nil {
				return fmt.Errorf("error setting slot %d as stable: %w", slot, err)
			}
			continue
		}

		log.Info("Continuing incomplete migration of slot", "slot", slot, "source", node.Name(), "destination", destNode.Name())

		if err := nodeList.MoveSlot(ctx, node, destNode, slot, MoveSlotOption{PurgeKeys: false}); err != nil {
			log.Error(err, "Failed to move slot", "slot", slot, "source", node.Name(), "target", destNode.Name())
			continue
		}

		node.RemoveMigrating(slot)
	}

	return nil
}

func (nodeList *ClusterNodeList) ReshardNode(ctx context.Context, source, target *ClusterNode, slots int, log logr.Logger) error {
	if slots == 0 {
		log.Info("No slots to reshard")
		return nil
	}

	log.Info("Resharding node", "source", source.Name(), "target", target.Name(), "slots", slots)

	var buf bytes.Buffer
	_, err := fmt.Fprintf(&buf, "/usr/bin/redis-cli --cluster reshard %s:6379 --cluster-from %s --cluster-to %s --cluster-slots %v --cluster-yes", source.GetIp(), source.ClusterNode.Name(), target.ClusterNode.Name(), slots)
	if err != nil {
		return err
	}

	command := buf.String()

	log.Info("Running command", "command", command)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Error(err, "Error running command", "command", command, "stderr", stderr.String(), "stdout", stdout.String())

		if strings.Contains(stderr.String(), "Please fix your cluster problems") {
			log.Info("Cluster is down, trying to fix it")
			err = nodeList.FixCluster(ctx, target, log)
			if err != nil {
				return err
			}
			log.Info("Cluster fixed, retrying reshard")
			return nodeList.ReshardNode(ctx, source, target, slots, log)
		}

		return err
	}

	log.Info("Command executed successfully", "command", command, "output", stdout.String())

	return nil
}

func (nodeList *ClusterNodeList) FixCluster(ctx context.Context, node *ClusterNode, log logr.Logger) error {
	log.Info("Fixing cluster", "node", node.Name())

	var buf bytes.Buffer
	_, err := fmt.Fprintf(&buf, "/usr/bin/redis-cli --cluster fix %s:6379", node.GetIp())
	if err != nil {
		return err
	}

	command := buf.String()

	log.Info("Running command", "command", command)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Error(err, "Error running command", "command", command, "stderr", stderr.String(), "stdout", stdout.String())

		return err
	}

	log.Info("Command executed successfully", "command", command, "output", stdout.String())

	return nil
}

func (nodeList *ClusterNodeList) ClearNodes(ctx context.Context, nodes []*ClusterNode, log logr.Logger, forgetNodes bool) error {
	weights := map[string]int{}
	for _, deletable := range nodes {
		weights[deletable.ClusterNode.Name()] = 0
	}

	slotMap, err := nodeList.buildSlotMap()
	if err != nil {
		return err
	}

	moveMapOptions := NewMoveMapOptions()
	moveMapOptions.weights = weights
	slotMoveMap, err := CalculateSlotMoveMap(slotMap, moveMapOptions)
	if err != nil {
		log.Error(err, "Error calculating SlotMoveMap")
		if errors.Is(err, ErrAllWeightsZero) {
			return nil
		}
		return err
	}

	slotMoveSequence := CalculateMoveSequence(slotMap, slotMoveMap, moveMapOptions)

	for _, moveSequence := range slotMoveSequence {
		err := nodeList.ReshardNode(ctx, moveSequence.FromNode, moveSequence.ToNode, len(moveSequence.Slots), log)
		if err != nil {
			return err
		}
		if forgetNodes {
			err = nodeList.ForgetNode(moveSequence.FromNode.Name())
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// RebalanceCluster rebalances the cluster based on given weights and options.
func (nodeList *ClusterNodeList) RebalanceCluster(ctx context.Context, weights map[string]int, options MoveSlotOption, log logr.Logger) error {
	if nodeList == nil {
		return fmt.Errorf("nodeList is nil")
	}

	masters, err := nodeList.GetMasters()
	if err != nil {
		return err
	}

	if len(masters) < 1 {
		return fmt.Errorf("no masters in node list")
	}

	node := masters[0]
	log.Info("Rebalancing cluster", "node", node.Name())

	var buf bytes.Buffer
	_, err = fmt.Fprintf(&buf, "/usr/bin/redis-cli --cluster rebalance %s:6379 --cluster-use-empty-masters", node.GetIp())

	if len(weights) > 0 {
		fmt.Fprintf(&buf, " --cluster-weight")

		for nodeId, weight := range weights {
			fmt.Fprintf(&buf, " %s=%v", nodeId, weight)
		}
	}

	command := buf.String()

	log.Info("Running command", "command", command)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Error(err, "Error running command", "command", command, "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	log.Info("Command executed successfully", "command", command, "output", stdout.String())

	// slotMap, err := nodeList.buildSlotMap()
	// if err != nil {
	// 	return err
	// }

	// moveMapOptions := NewMoveMapOptions()
	// moveMapOptions.weights = weights
	// slotMoveMap, err := CalculateSlotMoveMap(slotMap, moveMapOptions)
	// if err != nil {
	// 	log.Error(err, "Error calculating SlotMoveMap")
	// 	if errors.Is(err, ErrAllWeightsZero) {
	// 		return nil
	// 	}
	// 	return err
	// }

	// slotMoveSequence := CalculateMoveSequence(slotMap, slotMoveMap, moveMapOptions)

	// executeMoveSequence := func(waitGroup *sync.WaitGroup, moveSequence MoveSequence) error {
	// 	defer waitGroup.Done()
	// 	for _, slot := range moveSequence.Slots {
	// 		err := nodeList.MoveSlot(ctx, moveSequence.FromNode, moveSequence.ToNode, slot, options)
	// 		if err != nil {
	// 			log.Error(err, "Cannot move slot", "slot", slot, "source", moveSequence.FromNode.Name(), "target", moveSequence.ToNode.Name())
	// 			return err
	// 		}
	// 	}
	// 	return nil
	// }

	// var waitGroup sync.WaitGroup
	// errChan := make(chan error, len(slotMoveSequence))

	// for _, moveSequence := range slotMoveSequence {
	// 	waitGroup.Add(1)
	// 	go func(moveSequence MoveSequence) {
	// 		// errChan
	// 		errChan <- executeMoveSequence(&waitGroup, moveSequence)
	// 	}(moveSequence) // Doing more than one node's slot moves at the same time causes a panic in Redis Client.
	// }

	// waitGroup.Wait()
	// close(errChan)
	// var errors []error

	// for err := range errChan {
	// 	if err != nil {
	// 		errors = append(errors, err)
	// 	}
	// }

	// Firstly, fix the slots broken while migratin
	err = nodeList.EnsureClusterSlotsStable(ctx, log)
	if err != nil {
		return err
	}
	// Secondly, if there are errors when moving slots the cluster is not well Rebalanced, then, return error
	// if len(errors) > 0 {
	// 	return fmt.Errorf("moving slots errors: %v", errors)
	// }

	return nil
}

func (nodeList *ClusterNodeList) buildSlotMap() (map[*ClusterNode][]int, error) {
	slotMap := make(map[*ClusterNode][]int)
	masters, err := nodeList.GetMasters()
	if err != nil {
		return nil, err
	}
	for _, node := range masters {
		slotMap[node] = node.ClusterNode.Slots()
	}
	return slotMap, nil
}

func (nodeList *ClusterNodeList) NeedsClusterMeet(ctx context.Context) (bool, error) {
	// Compile a map of all the IPs which should be listed for each node.
	// We are using a map to make it faster, as searching a has table is better than a list
	ipList := map[string]struct{}{}
	for _, node := range nodeList.Nodes {
		ipList[node.Pod.Status.PodIP] = struct{}{}
	}

	// Now for every node, make sure that the nodes it knows about, is the same as the nodes we know about.
	for _, node := range nodeList.Nodes {
		clusterNodes, err := node.ClusterNode.R().Do(ctx, "CLUSTER", "NODES").Text()
		if err != nil {
			return false, err
		}
		clusterNodeStrings := strings.Split(clusterNodes, "\n")
		clusterNodeCount := 0
		// name addr flags role ping_sent ping_recv link_status slots
		for _, val := range clusterNodeStrings {
			parts := strings.Split(val, " ")
			if len(parts) <= 3 {
				continue
			}

			clusterNodeCount++
			addr := strings.Split(parts[1], "@")[0]
			host, _, _ := net.SplitHostPort(addr)
			// If the IP does not exist in our list,
			// we are probably using an outdated one and should ClusterMeet.
			if _, ok := ipList[host]; !ok {
				return true, nil
			}
		}
		// Every cluster node should see all the nodes.
		// If a node has forgotten any other node we need to meet nodes.
		if len(nodeList.Nodes) > clusterNodeCount {
			return true, nil
		}
	}
	return false, nil
}

func (nodeList *ClusterNodeList) EnsureReplicaSpread(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	activeMasters, err := nodeList.GetMasters()
	if err != nil {
		return err
	}
	var masterNeedsReplicas []*ClusterNode
	var replicaNeedsMove []*ClusterNode

	err = nodeList.LoadInfoForNodes()
	if err != nil {
		return err
	}

	for _, master := range activeMasters {
		replicas, err := nodeList.GetMasterReplicas(master)
		if err != nil {
			return err
		}
		if len(replicas) == int(redisCluster.Spec.ReplicasPerMaster) {
			continue
		} else if len(replicas) < int(redisCluster.Spec.ReplicasPerMaster) {
			// Too few replicas
			masterNeedsReplicas = append(masterNeedsReplicas, master)
		} else if len(replicas) > int(redisCluster.Spec.ReplicasPerMaster) {
			// Too few replicas
			replicaNeedsMove = append(replicaNeedsMove, replicas[:redisCluster.Spec.ReplicasPerMaster]...)
		}
	}

	// There might be replicas which are replicating replicas. We want to change these to point at masters
	replicas, err := nodeList.GetReplicas()
	if err != nil {
		return err
	}
	for _, replica := range replicas {
		replicasPointedAtReplicas, err := nodeList.GetMasterReplicas(replica)
		if err != nil {
			return err
		}
		replicaNeedsMove = append(replicaNeedsMove, replicasPointedAtReplicas...)
	}

	// There might be replicas which are replicating masters which we cannot see.
	masters, err := nodeList.GetMasters()
	if err != nil {
		return err
	}
	for _, replica := range replicas {
		// Is the replica pointing at one of the masters ?
		pointedAtMaster := false
		for _, master := range masters {
			if master.ClusterNode.Name() == replica.ClusterNode.Replicate() {
				pointedAtMaster = true
			}
		}
		if !pointedAtMaster {
			replicaNeedsMove = append(replicaNeedsMove, replica)
		}
	}

	for i := 0; i < len(masterNeedsReplicas); i++ {
		_, err := replicaNeedsMove[i].ClusterNode.ClusterReplicateWithNodeID(masterNeedsReplicas[i].ClusterNode.Name())
		if err != nil {
			return err
		}
	}

	return nil
}

func (nodeList *ClusterNodeList) EnsureClusterRatio(ctx context.Context, redisCluster *redisv1.RedisCluster, log logr.Logger) error {
	// When all the nodes are ready in the statefulset,
	// we need to make sure there is the right ratio of masters and replicas for the redis cluster
	// If there are too many replicas, we need to reset and add as a master
	//
	// If there are too few replicas, we need to reset and add as a
	// replica of a master with the least amount of replicas attached
	activeMasters, err := nodeList.GetMasters()
	if err != nil {
		return err
	}
	activeReplicas, err := nodeList.GetReplicas()
	if err != nil {
		return err
	}
	sort.Slice(activeMasters, func(i, j int) bool {
		return len(activeMasters[i].Slots()) > len(activeMasters[j].Slots())
	})

	// Using an exit early strategy. Start with the best case.
	if len(activeMasters) == int(redisCluster.Spec.Replicas) {
		err := nodeList.EnsureReplicaSpread(ctx, redisCluster)
		return err
	}
	if len(activeMasters) > int(redisCluster.Spec.Replicas) {
		// TODO We need to keep the masters with the most slots, and the ones that already have replicas
		sort.Slice(activeMasters, func(i, j int) bool {
			return len(activeMasters[i].Slots()) > len(activeMasters[j].Slots())
		})
		fmt.Printf("Sorted masters %v\n", activeMasters)
		keepMasters := activeMasters[:int(redisCluster.Spec.Replicas)]
		deleteMasters := activeMasters[int(redisCluster.Spec.Replicas):]

		// We want to ensure the slots are all stable before we try to rebalance
		err = nodeList.EnsureClusterSlotsStable(ctx, log)
		if err != nil {
			return err
		}

		weights := map[string]int{}
		for _, deletable := range deleteMasters {
			weights[deletable.ClusterNode.Name()] = 0
		}
		err = nodeList.RebalanceCluster(ctx, weights, MoveSlotOption{PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance}, log)
		if err != nil {
			return err
		}

		// We can now reset the deletable masters to replicas of the masters
		// Find masters with too few replicas
		currentKeepable := 0
		time.Sleep(10 * time.Second)
		for _, master := range deleteMasters {
			_, err := master.ClusterNode.ClusterReplicateWithNodeID(keepMasters[currentKeepable].ClusterNode.Name())
			if err != nil {
				return err
			}
			currentKeepable = currentKeepable + 1
			if currentKeepable > int(redisCluster.Spec.Replicas)-1 {
				currentKeepable = 0
			}
		}
		// At this point the nodes info is outdated and needs to be updated
		err = nodeList.LoadInfoForNodes()
		if err != nil {
			return err
		}

		err = nodeList.EnsureReplicaSpread(ctx, redisCluster)
		return err
	}
	if len(activeMasters) < int(redisCluster.Spec.Replicas) && redisCluster.Spec.ReplicasPerMaster > 0 {

		// Get replicas to change into masters
		needsReplicas := int(redisCluster.Spec.Replicas) - len(activeMasters)
		convertableReplicas := activeReplicas[:needsReplicas]
		for _, replica := range convertableReplicas {
			err := replica.ClusterNode.Call("cluster", "reset").Err()
			if err != nil {
				return err
			}
			// Wait for reset
			time.Sleep(5 * time.Second)
		}

		err = nodeList.ClusterMeet(ctx)
		if err != nil {
			return err
		}

		// Wait for cluster meet so nodes can agree on configuration
		time.Sleep(10 * time.Second)

		err = nodeList.LoadInfoForNodes()
		if err != nil {
			return err
		}

		err = nodeList.EnsureReplicaSpread(ctx, redisCluster)
		return err
	}
	if len(activeReplicas) > 0 && redisCluster.Spec.ReplicasPerMaster == 0 {
		for _, replica := range activeReplicas {
			err := replica.ClusterNode.Call("cluster", "reset").Err()
			if err != nil {
				return err
			}
			// Wait for reset
			time.Sleep(5 * time.Second)
		}

		err = nodeList.ClusterMeet(ctx)
		if err != nil {
			return err
		}

		// Wait for cluster meet so nodes can agree on configuration
		time.Sleep(10 * time.Second)

		err = nodeList.LoadInfoForNodes()
		if err != nil {
			return err
		}

		err = nodeList.EnsureReplicaSpread(ctx, redisCluster)
		return err
	}

	return nil
}

func (nodeList *ClusterNodeList) RemoveClusterOutdatedNodes(ctx context.Context, redisCluster *redisv1.RedisCluster, log logr.Logger) error {
	// If a node is listed in the nodes.conf file of a Redis node,
	// but it is no longer applicable due to a restart and receiving a new IP from Kubernetes,
	// we can consider the Node outdated.
	//
	// We need to find any nodes that are outdated due to restart in ephemeral mode.
	//
	// If there are the correct amount of pods,
	// and the same amount of successful nodes,
	// but > 0 failing nodes,
	// we can safely assume that the failing node is due to a restart,
	// and optimistically delete it from the set of nodes in redis.
	listOptions := ctrlClient.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel: redisCluster.Name,
				GetStatefulSetSelectorLabel(ctx, nodeList.client, redisCluster): "redis",
			},
		),
	}

	podsReady, err := utils.AllPodsReady(ctx, nodeList.client, &listOptions, redisCluster.NodesNeeded())
	if err != nil {
		return err
	}
	if !podsReady || len(nodeList.Nodes) != redisCluster.NodesNeeded() {
		return nil
	}

	if err = nodeList.LoadInfoForNodes(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, node := range nodeList.Nodes {
		for _, friend := range node.Friends() {
			if friend.HasFlag("fail") || friend.HasFlag("noaddr") {
				wg.Add(1)
				go func(node *ClusterNode, friend *redis.NodeInfo) {
					defer wg.Done()
					if _, err := node.ClusterNode.ClusterForgetNodeID(friend.NodeID()); err != nil {
						log.Info("Node not deleted cause new node or have been deleted.")
					}
				}(node, friend)
			}
		}
	}
	wg.Wait()
	return nil
}

func (nodeList *ClusterNodeList) ForgetNode(nodeId string) error {
	for _, node := range nodeList.Nodes {
		if node.ClusterNode.Name() == nodeId {
			continue
		}
		err := node.ClusterNode.Call("cluster", "forget", nodeId).Err()
		if err != nil {
			if !strings.Contains(err.Error(), "ERR Unknown node") {
				return err
			}
		}
	}
	return nil
}

func (nodeList *ClusterNodeList) AssignMissingSlots() error {
	// First we need to get all the nodes
	// Then calculate which slots are assigned
	// We then need to subtract this from all the slots.
	// Then we need to loop over the unassigned slots,
	// and add them to the nodes with the least amount of assigned slots

	masterNodes, err := nodeList.GetMasters()
	if err != nil {
		return err
	}

	// We start with a map so we can easily delete slots if they are already assigned
	allSlots := utils.MakeRangeMap(0, 16383)
	for _, node := range masterNodes {
		for _, slot := range node.ClusterNode.Slots() {
			// We want to remove any assigned slots from our list so we end up with unassigned slots
			delete(allSlots, slot)
		}
	}
	totalMasterNodes := len(masterNodes)
	slotsPerNode := CalculateMaxSlotsPerMaster(RedisClusterTotalSlots, totalMasterNodes)

	// We convert the slots left over in the map to an array so we can easily sort them,
	// and cut off slices
	var slotsToAssign []int
	for slot := range allSlots {
		slotsToAssign = append(slotsToAssign, slot)
	}
	sort.Ints(slotsToAssign)

	for i, node := range masterNodes {
		slotAmountToAssign := slotsPerNode - len(node.ClusterNode.Slots())
		if slotAmountToAssign <= 0 {
			continue
		}
		// If we reach the last master node with a number of not assigned slots greater than slotsPerNodes
		// (mainly when configuring a new redis cluster) the exceeding slots will be assigned to this node.
		var slotList []int
		if i == totalMasterNodes-1 || len(slotsToAssign) <= slotAmountToAssign {
			slotList = slotsToAssign[0:]
			slotsToAssign = []int{}
		} else {
			slotList = slotsToAssign[0:slotAmountToAssign]
			slotsToAssign = slotsToAssign[slotAmountToAssign:]
		}

		var strSlotList []interface{}
		for _, slot := range slotList {
			strSlotList = append(strSlotList, strconv.Itoa(slot))
		}

		if len(strSlotList) > 0 {
			_, err := node.ClusterNode.ClusterAddSlots(strSlotList...)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (nodeList *ClusterNodeList) MoveSlot(ctx context.Context, sourceNode *ClusterNode, targetNode *ClusterNode, slot int, options MoveSlotOption) error {
	if err := prepareSlotMigration(sourceNode, targetNode, slot); err != nil {
		return fmt.Errorf("preparing slot migration: %w", err)
	}

	if err := migrateSlotKeys(ctx, sourceNode, targetNode, slot, options); err != nil {
		return fmt.Errorf("migrating slot keys: %w", err)
	}

	if err := finalizeSlotMigration(nodeList, slot, targetNode); err != nil {
		return fmt.Errorf("finalizing slot migration: %w", err)
	}

	return nil
}

func prepareSlotMigration(sourceNode, targetNode *ClusterNode, slot int) error {
	err := setSlotState(targetNode, RedisImportingState, slot, sourceNode.ClusterNode.Name())
	if err != nil {
		return err
	}
	err = setSlotState(sourceNode, RedisMigratingState, slot, targetNode.ClusterNode.Name())
	if err != nil {
		return err
	}
	return nil
}

func migrateSlotKeys(ctx context.Context, sourceNode, targetNode *ClusterNode, slot int, options MoveSlotOption) error {
	for {
		keys, err := sourceNode.R().ClusterGetKeysInSlot(ctx, slot, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			break
		}

		if options.PurgeKeys {
			if err := purgeKeys(sourceNode, keys); err != nil {
				return err
			}
		} else {
			if err := migrateKeys(sourceNode, targetNode, keys); err != nil {
				return err
			}
		}
	}
	return nil
}

func finalizeSlotMigration(nodeList *ClusterNodeList, slot int, targetNode *ClusterNode) error {
	return setSlotForMasters(nodeList, slot, targetNode)
}

func setSlotState(node *ClusterNode, state string, slot int, targetNodeName string) error {
	return node.Call(RedisClusterCommand, RedisSetSlotSubCommand, slot, state, targetNodeName).Err()
}

func purgeKeys(node *ClusterNode, keys []string) error {
	var cmd []interface{}
	cmd = append(cmd, RedisDeleleCommand)
	for _, key := range keys {
		cmd = append(cmd, key)
	}
	_, err := node.Call(cmd...).Result()
	if err != nil && !strings.Contains(err.Error(), RedisAskRedirectionResponse) {
		// If we get an ask redirection,
		// it means that the cluster wants us to ask the new node most probably.
		// When deleting keys, we do it to make the migration faster.
		// If we run against the new node, there is no good reason for deleting the keys anymore,
		// as the primary purpose of the delete has been served.
		// We can therefor ignore the ASK redirection, and simply continue to the next piece of logic.
		// In this case we got a different error, and definitely want to fail hard and fast
		// to void putting the Redis cluster in an unfixable state.
		//r.Log.Error(err, "Failed to delete keys", "cmd", cmd)
		return err
	}
	return nil
}

// MigrateKeys migrates the given keys from sourceNode to targetNode.
func migrateKeys(sourceNode, targetNode *ClusterNode, keys []string) error {
	if len(keys) == 0 {
		// No keys to migrate
		return nil
	}

	migrateCmd := buildMigrateCommand(targetNode, RedisKeysOption, keys)

	_, err := sourceNode.Call(migrateCmd...).Text()
	if err != nil && strings.Contains(err.Error(), RedisBusyKeyResponse) {
		// Retry with REPLACE option
		migrateCmd = buildMigrateCommand(targetNode, RedisReplaceOption, keys)
		err = sourceNode.Call(migrateCmd...).Err()
	}

	if err != nil {
		return fmt.Errorf("could not move keys to new node: %w", err)
	}

	return nil
}

// buildMigrateCommand builds the Redis MIGRATE command.
func buildMigrateCommand(targetNode *ClusterNode, option string, keys []string) []interface{} {
	cmd := []interface{}{
		RedisCommandMigrate,
		targetNode.ClusterNode.Host(),
		strconv.Itoa(redis.RedisCommPort),
		"", 0, MigrateTimeout,
		option,
	}
	for _, key := range keys {
		cmd = append(cmd, key)
	}
	return cmd
}

// SetSlotForMasters sets a new node for a given slot in all master nodes.
func setSlotForMasters(nodeList *ClusterNodeList, slot int, targetNode *ClusterNode) error {
	masters, err := nodeList.GetMasters()
	if err != nil {
		return err
	}

	// Acording to documentation first, handle the targetNode if it's a master
	if targetNode.IsMaster() {
		if err := setSlotForNode(targetNode, slot, targetNode); err != nil {
			return fmt.Errorf("could not set slot %d for target node %s: %w", slot, targetNode.Name(), err)
		}
	}

	// Then, handle other master nodes
	for _, node := range masters {
		if node != targetNode && node.IsMaster() {
			if err := setSlotForNode(node, slot, targetNode); err != nil {
				return fmt.Errorf("could not set slot %d for node %s with target %s: %w", slot, node.Name(), targetNode.Name(), err)
			}
		}
	}

	return nil
}

// setSlotForNode sets a given slot for the specified node.
func setSlotForNode(node *ClusterNode, slot int, targetNode *ClusterNode) error {
	return node.Call(RedisClusterCommand, RedisSetSlotSubCommand, slot, RedisNodeSubCommand, targetNode.ClusterNode.Name()).Err()
}

// NewMoveMapOptions creates a new instance of MoveMapOptions with default values.
func NewMoveMapOptions() *MoveMapOptions {
	return &MoveMapOptions{
		threshold: RedisNodesUnbalancedThreshold,
		weights:   make(map[string]int),
	}
}

// GetNodeWeight retrieves the weight of a node by its name.
func (opt *MoveMapOptions) GetNodeWeight(nodeId string) int {
	if weight, ok := opt.weights[nodeId]; ok {
		return weight
	}
	return 1
}

// SetWeights sets the weights for the nodes.
func (opt *MoveMapOptions) SetWeights(weights map[string]int) {
	opt.weights = weights
}

// SetThreshold sets the threshold value.
func (opt *MoveMapOptions) SetThreshold(threshold int) {
	opt.threshold = threshold
}

func CalculateSlotMoveMap(slotMap map[*ClusterNode][]int, options *MoveMapOptions) (map[*ClusterNode][]int, error) {
	if len(slotMap) == 0 {
		return nil, fmt.Errorf("slot map is empty")
	}
	if options == nil {
		return nil, fmt.Errorf("options are nil")
	}

	totalSlots, totalWeight := calculateTotalWeight(slotMap, options)
	if totalWeight == 0 {
		return nil, ErrAllWeightsZero
	}

	slotsPerWeight := CalculateMaxSlotsPerMaster(totalSlots, totalWeight)
	result := make(map[*ClusterNode][]int)

	for node, slots := range slotMap {
		assignSlotsToNode(node, slots, slotsPerWeight, options, result)
	}

	return result, nil
}

func calculateTotalWeight(slotMap map[*ClusterNode][]int, options *MoveMapOptions) (int, int) {
	totalSlots := 0
	totalWeight := 0
	for node, slots := range slotMap {
		totalSlots += len(slots)
		totalWeight += options.GetNodeWeight(node.Name())
	}
	return totalSlots, totalWeight
}

func assignSlotsToNode(node *ClusterNode, slots []int, slotsPerWeight int, options *MoveMapOptions, result map[*ClusterNode][]int) {
	weight := options.GetNodeWeight(node.Name())
	if weight == 0 {
		result[node] = slots
		return
	}

	shouldHaveSlots := slotsPerWeight * weight

	nodeShouldSend := hasLeftoverSlotsOverThreshold(len(slots), shouldHaveSlots, options.threshold)

	if nodeShouldSend {
		result[node] = slots[shouldHaveSlots:]
	} else {
		result[node] = []int{}
	}
}

func CalculateMoveSequence(SlotMap map[*ClusterNode][]int, SlotMoveMap map[*ClusterNode][]int, options *MoveMapOptions) []MoveSequence {
	hashMap := make(map[string]MoveSequence)

	totalSlots := 0
	totalWeight := 0
	for node, slots := range SlotMap {
		totalSlots += len(slots)
		totalWeight += options.GetNodeWeight(node.Name())
	}
	slotsPerWeight := CalculateMaxSlotsPerMaster(totalSlots, totalWeight)

	// Leftover string protects against rounding errors where a 0 weighted node could still have slots.
	// The actual leftover is the "last node with weight > 0", where we can move any leftover
	// slots to to protect 0 weighted slots
	var leftOverNode *ClusterNode
	for destination, destinationSlots := range SlotMap {
		destinationWeight := options.GetNodeWeight(destination.Name())
		if destinationWeight == 0 {
			continue
		}
		destinationShouldHaveSlots := slotsPerWeight * destinationWeight
		if len(destinationSlots) < destinationShouldHaveSlots {
			// Destination needs slots
			needsSlots := destinationShouldHaveSlots - len(destinationSlots)
			for source, slots := range SlotMoveMap {
				if source == destination {
					// No point trying to take slots from ourself
					continue
				}
				if len(slots) == 0 {
					// No point trying to steal slots from poor sources
					continue
				}

				var takeSlots []int
				if len(slots) <= needsSlots {
					takeSlots = slots
					SlotMoveMap[source] = make([]int, 0)
				} else {
					takeSlots = slots[:needsSlots]
					SlotMoveMap[source] = slots[needsSlots:]
				}
				key := source.Name() + ":" + destination.Name()
				if _, ok := hashMap[key]; ok {
					hashMap[key] = MoveSequence{
						From:     hashMap[key].From,
						FromNode: hashMap[key].FromNode,
						To:       hashMap[key].To,
						ToNode:   hashMap[key].ToNode,
						Slots:    append(hashMap[key].Slots, takeSlots...),
					}
				} else {
					hashMap[key] = MoveSequence{
						From:     source.Name(),
						FromNode: source,
						To:       destination.Name(),
						ToNode:   destination,
						Slots:    takeSlots,
					}
				}
				needsSlots -= len(takeSlots)
				if needsSlots == 0 {
					break
				}
			}
			leftOverNode = destination
		}
	}
	for source, slots := range SlotMoveMap {
		// We need to move any slots into the last destination node with weight > 1
		if len(slots) > 0 {
			if source.Name() == leftOverNode.Name() {
				// No point trying to take slots from ourself, we might as well leave them there
				SlotMoveMap[source] = []int{}
				continue
			}
			key := source.Name() + ":" + leftOverNode.Name()
			if _, ok := hashMap[key]; ok {
				hashMap[key] = MoveSequence{
					From:     hashMap[key].From,
					FromNode: hashMap[key].FromNode,
					To:       hashMap[key].To,
					ToNode:   hashMap[key].ToNode,
					Slots:    append(hashMap[key].Slots, slots...),
				}
			} else {
				hashMap[key] = MoveSequence{
					From:     source.Name(),
					FromNode: source,
					To:       leftOverNode.Name(),
					ToNode:   leftOverNode,
					Slots:    slots,
				}
			}

		}
	}

	result := make([]MoveSequence, 0)

	for _, moveSequence := range hashMap {
		result = append(result, moveSequence)
	}

	return result
}

func (nodeList *ClusterNodeList) GetUnbalancedNodes(ctx context.Context) (map[string]int, error) {
	unbalancedNodes := make(map[string]int)
	masterNodes, err := nodeList.GetMasters()
	if err != nil {
		return unbalancedNodes, err
	}
	slotsPerMasterNode := CalculateMaxSlotsPerMaster(RedisClusterTotalSlots, len(masterNodes))
	for _, node := range masterNodes {
		slots := len(node.Slots())
		if hasLeftoverSlotsOverThreshold(slots, slotsPerMasterNode, RedisNodesUnbalancedThreshold) {
			unbalancedNodes[node.Name()] = slots - slotsPerMasterNode
		}
	}
	return unbalancedNodes, nil
}

func CalculateMaxSlotsPerMaster(slots int, masters int) int {
	return int(math.Ceil(float64(slots) / float64(masters)))
}

func hasLeftoverSlotsOverThreshold(slots int, slotsPerNode int, threshold int) bool {
	leftoverSlots := slots - slotsPerNode
	return leftoverSlots > slotsPerNode*threshold/100
}
