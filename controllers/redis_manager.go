// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	redis "github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/robin"
	"github.com/inditextech/redisoperator/internal/utils"

	"github.com/go-logr/logr"
	errors2 "k8s.io/apimachinery/pkg/api/errors"

	redisclient "github.com/redis/go-redis/v9"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ConfigChecksumAnnotation = "inditex.dev/redis-conf"
)

// Redis cluster is set to 0 replicas
//
//	 -> terminate all cluster pods (StatefulSet replicas set to 0)
//	 -> terminate robin pod (Deployment replicas set to 0)
//	 -> delete pdb
//	 -> Redis cluster status set to 'Ready'
//		-> All conditions set to false
func (r *RedisClusterReconciler) clusterScaledToZeroReplicas(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	r.logInfo(redisCluster.NamespacedName(), "Cluster spec replicas is set to 0", "SpecReplicas", redisCluster.Spec.Replicas)
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Cannot find exists statefulset maybe is deleted.")
	}
	if sset != nil {
		if *(sset.Spec.Replicas) != 0 {
			r.logInfo(redisCluster.NamespacedName(), "Cluster scaled to 0 replicas")
			r.Recorder.Event(redisCluster, "Normal", "RedisClusterScaledToZero", fmt.Sprintf("Scaling down from %d to 0", *(sset.Spec.Replicas)))
		}
		*sset.Spec.Replicas = 0
		sset, err = r.updateStatefulSet(ctx, sset, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		}
		r.logInfo(redisCluster.NamespacedName(), "StatefulSet updated", "Replicas", sset.Spec.Replicas)
	}

	r.scaleDownRobin(ctx, redisCluster)

	r.deletePodDisruptionBudget(ctx, redisCluster)

	// All conditions set to false. Status set to Ready.
	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, redisv1.StatusReady) {
		redisCluster.Status.Status = redisv1.StatusReady
		setAllConditionsFalse(r.getHelperLogger(redisCluster.NamespacedName()), redisCluster)
		update_err = r.updateClusterStatus(ctx, redisCluster)
	}

	return update_err
}

func (r *RedisClusterReconciler) upgradeCluster(ctx context.Context, redisCluster *redisv1.RedisCluster) error {

	// If a Fast Upgrade is possible or already en progress, start it or check if it's already finished.
	// In both situations we return here.
	fastUpgrade, err := r.doFastUpgrade(ctx, redisCluster)
	if err != nil || fastUpgrade {
		return err
	}

	// Continue doing a Slow Upgrade if we can't go the Fast way.
	return r.doSlowUpgrade(ctx, redisCluster)
}

// If PurgeKeysOnRebalance flag is active and Redis cluster is not configures as master-replica we can
// do a Fast Upgrade, applying the changes to the StatefulSet and recreaing it. Slots move will be avoided.
func (r *RedisClusterReconciler) doFastUpgrade(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	switch redisCluster.Status.Substatus.Status {
	case redisv1.SubstatusFastUpgrading:
		// Already Fast upgrading. Check if the node pods are ready to start rebuilding the cluster.
		r.logInfo(redisCluster.NamespacedName(), "Retaking Fast Upgrade")

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      redisCluster.Name,
				Namespace: redisCluster.Namespace,
			},
		}
		existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
		if err != nil {
			return false, err
		}
		logger := r.getHelperLogger(redisCluster.NamespacedName())

		podsReady, err := r.allPodsReady(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready to end Fast Upgrade", "expectedReplicas", int(*(existingStatefulSet.Spec.Replicas)))
			return true, nil
		}

		// Rebuild the cluster
		robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		err = robin.ClusterFix()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error performing a cluster fix through Robin")
			return true, err
		}

		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusFastUpgradeFinalizing, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil

	case redisv1.SubstatusFastUpgradeFinalizing:
		// Rebuilding the cluster after recreating all node pods. Check if the cluster is ready to end the Fast upgrade.
		logger := r.getHelperLogger(redisCluster.NamespacedName())
		robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		check, errors, warnings, err := robin.ClusterCheck()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			r.logInfo(redisCluster.NamespacedName(), "Waiting for Redis cluster readiness before ending the fast upgrade", "errors", errors, "warnings", warnings)
			return true, nil
		}

		// The cluster is now upgraded, and we can set the Cluster Status as Ready again and remove the Substatus.
		redisCluster.Status.Status = redisv1.StatusReady
		redisCluster.Status.Substatus.Status = ""
		setConditionFalse(logger, redisCluster, redisv1.ConditionUpgrading)

		return true, nil
	default:
		// Fast upgrade start: If purgeKeysOnRebalance property is set to 'true' and we have no replicas. No need to iterate over the partitions.
		// If fast upgrade is not allowed but we have no replicas, the cluster must be scaled up adding one extra
		// node to be able to move slots and keys in order to ensure keys are preserved.
		if redisCluster.Spec.PurgeKeysOnRebalance && redisCluster.Spec.ReplicasPerMaster == 0 {
			r.logInfo(redisCluster.NamespacedName(), "Fast upgrade will be performed")

			err := r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusFastUpgrading, 0)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}

			// Update configuration: changes in configuration, labels and overrides are persisted before upgrading
			existingStatefulSet, err := r.upgradeClusterConfigurationUpdate(ctx, redisCluster)
			if err != nil {
				return true, err
			}

			// ** FAST UPGRADE start **
			r.Client.Delete(ctx, existingStatefulSet)

			return true, nil
		}
	}

	return false, nil
}

// Classic Slow Upgrade: StatefulSet updated with the config changes, cluster is scaledUp if no replicas used,
// slots and keys are copied. No data lose.
func (r *RedisClusterReconciler) doSlowUpgrade(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redisCluster.Name,
			Namespace: redisCluster.Namespace,
		},
	}

	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}
	logger := r.getHelperLogger(redisCluster.NamespacedName())

	// We need to ensure that the upgrade is really necessary,
	// and we are not just host to a double reconciliation attempt
	if redisCluster.Status.Substatus.Status == "" {
		err = r.updateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			return fmt.Errorf("could not update upgrading status: %w", err)
		}
	}
	if redisCluster.Status.Status != redisv1.StatusUpgrading {
		// If the upgrade is no longer necessary, we can stop here and return.
		// We also need to make sure that the partition is not higher than 0,
		// As that would mean we are in the midst of an upgrade and should continue upgrading regardless
		if !reflect.DeepEqual(existingStatefulSet.Spec.UpdateStrategy, v1.StatefulSetUpdateStrategy{}) {
			if int(*(existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)) == 0 {
				return nil
			}
		} else {
			return nil
		}
	}

	// ScaleUp the cluster before upgrading
	scaledBeforeUpgrade := false
	if !redisCluster.Spec.PurgeKeysOnRebalance && redisCluster.Spec.ReplicasPerMaster == 0 {
		r.logInfo(redisCluster.NamespacedName(), "Scaling Up the cluster before upgrading")
		clusterScaled, err := r.scaleUpForUpgrade(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when scaling up before upgrade")
			return err
		}
		if !clusterScaled {
			// cluster not scaled yet, return to requeue and wait for pods readiness
			return nil
		}
		scaledBeforeUpgrade = true
	}

	if redisCluster.Status.Substatus.Status == "" || redisCluster.Status.Substatus.Status == redisv1.SubstatusUpgradingScaleUp {
		// Do not update substatus if we are resuming an upgrade
		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusSlowUpgrading, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	}

	if redisCluster.Status.Substatus.Status == redisv1.SubstatusSlowUpgrading {

		// Update labels, configuration, annotations, overrides, etc.
		existingStatefulSet, err = r.upgradeClusterConfigurationUpdate(ctx, redisCluster)
		if err != nil {
			return err
		}

		clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
			return err
		}
		// Free cluster nodes to avoid memory consumption
		defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

		err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
		if err != nil {
			return err
		}

		startingPartition := int(*(existingStatefulSet.Spec.Replicas)) - 1

		if !reflect.DeepEqual(existingStatefulSet.Spec.UpdateStrategy, v1.StatefulSetUpdateStrategy{}) {
			// An UpdateStrategy is already set.
			// We can reuse the partition as the starting partition
			// We should only do this for partitions higher than zero,
			// as we cannot unset the partition after an upgrade, and the default will be zero.
			//
			// So 0 partition will trigger a bad thing, where the whole cluster will be immediately applied,
			// and all pods destroyed simultaneously.
			// We need to only set the starting partition if it is higher than 0,
			// otherwise use the replica count one from before.
			// Unless we are resuming an upgrade after an error when scaling down.
			if int(*(existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)) > 0 ||
				redisCluster.Status.Substatus.Status == redisv1.SubstatusUpgradingScaleDown {
				r.logInfo(redisCluster.NamespacedName(), "Update Strategy already set. Reusing partition", "partition", existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)
				startingPartition = int(*(existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition))
			}
		}

		if redisCluster.Spec.ReplicasPerMaster == 0 {
			// Loop over the cluster partitions upgrading once at a time
			for partition := startingPartition; partition >= 0; partition-- {
				err = r.UpgradePartition(ctx, redisCluster, existingStatefulSet, partition, logger)
				if err != nil {
					return err
				}
			}
		} else if redisCluster.Spec.ReplicasPerMaster > 0 {
			// We have replicas in this cluster
			// We don't need to rebalance, we only need to failover if master, and restart and re-replicate if replica
			for partition := startingPartition; partition >= 0; partition-- {
				err = r.UpgradePartitionWithReplicas(ctx, redisCluster, existingStatefulSet, partition, logger)
				if err != nil {
					return err
				}
			}
		}
	}

	// Scale down the cluster if an extra node where added before upgrading
	if scaledBeforeUpgrade {
		r.logInfo(redisCluster.NamespacedName(), "Scaling down after the upgrade")
		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusUpgradingScaleDown, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
		redisCluster.Spec.Replicas--
		_, err = r.scaleCluster(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error scaling down after the upgrade")
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Scaling down after the upgrade completed")
	}

	err = r.updateClusterSubStatus(ctx, redisCluster, "", 0)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	// We want to sleep to make sure the k8s client
	// gets a chance to update the pod list in the cache before we try to rebalance again,
	// otherwise it gets an outdated set of IPs
	time.Sleep(5 * time.Second)

	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	err = clusterNodes.RebalanceCluster(ctx, map[string]int{}, kubernetes.MoveSlotOption{
		PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
	}, logger)
	if err != nil {
		return err
	}

	// The cluster is now upgraded, and we can set the Cluster Status as Ready again and remove the Substatus.
	redisCluster.Status.Status = redisv1.StatusReady
	redisCluster.Status.Substatus.Status = ""
	setConditionFalse(logger, redisCluster, redisv1.ConditionUpgrading)

	return nil
}

// Updates and persists the StatefulSet configuration and labels, including overrides.
func (r *RedisClusterReconciler) upgradeClusterConfigurationUpdate(ctx context.Context, redisCluster *redisv1.RedisCluster) (*v1.StatefulSet, error) {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redisCluster.Name,
			Namespace: redisCluster.Namespace,
		},
	}

	// RedisCluster .Spec.Labels
	mergedLabels := *redisCluster.Spec.Labels
	defaultLabels := map[string]string{
		redis.RedisClusterLabel:                     redisCluster.Name,
		r.getStatefulSetSelectorLabel(redisCluster): "redis",
	}
	maps.Copy(mergedLabels, defaultLabels)

	// Get configmap
	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		return nil, err
	}
	configMap.Labels = mergedLabels

	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(redisCluster)
	r.logInfo(redisCluster.NamespacedName(), "Updating configmap", "configmap", configMap.Name)
	// Update ConfigMap
	err = r.Client.Update(ctx, configMap)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when creating configmap")
		return nil, err
	}

	// Get Service
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		return nil, err
	}
	// Update the Service labels
	maps.Copy(service.Labels, mergedLabels)
	r.logInfo(redisCluster.NamespacedName(), "Updating service", "service", service.Name)
	err = r.Client.Update(ctx, service)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when updating service")
	}

	// Get existing StateFulSet
	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return nil, err
	}
	r.logInfo(redisCluster.NamespacedName(), "Updating statefulset", "StateFulSet", existingStatefulSet.Name)
	// Update StateFulSet .Spec.Template.Labels
	existingStatefulSet.Labels = mergedLabels

	// Add labels from override
	if redisCluster.Spec.Override.StatefulSet != nil && redisCluster.Spec.Override.StatefulSet.Spec.Template.Labels != nil {
		maps.Copy(mergedLabels, redisCluster.Spec.Override.StatefulSet.Spec.Template.Labels)
	}

	existingStatefulSet.Spec.Template.ObjectMeta.Labels = mergedLabels
	// Update StateFulSet .Spec.Template.Spec.Containers[0].Resources
	if redisCluster.Spec.Resources != nil && existingStatefulSet.Spec.Template.Spec.Containers != nil && len(existingStatefulSet.Spec.Template.Spec.Containers) > 0 {
		existingStatefulSet.Spec.Template.Spec.Containers[0].Resources = *redisCluster.Spec.Resources
		existingStatefulSet.Spec.Template.Spec.Containers[0].Image = redisCluster.Spec.Image
	}
	// Update StatefulSet annotations with calculated config checksum if needed
	existingStatefulSet = r.addConfigChecksumAnnotation(existingStatefulSet, redisCluster)

	// Handle changes in spec.override
	existingStatefulSet, _ = r.overrideStatefulSet(req, redisCluster, existingStatefulSet)

	return existingStatefulSet, nil
}

func (r *RedisClusterReconciler) UpgradePartition(ctx context.Context, redisCluster *redisv1.RedisCluster, existingStateFulSet *v1.StatefulSet, partition int, logger logr.Logger) error {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	r.logInfo(redisCluster.NamespacedName(), "Looping partitions for Rolling Update", "partition", partition)
	node, err := clusterNodes.GetNodeByPodName(fmt.Sprintf("%s-%d", existingStateFulSet.Name, partition))
	if err != nil {
		return err
	}

	// Empty the node by moving its slots to another node.
	// If any slot cannot be moved, it will be retried.
	for retry := 3; retry >= 0; retry-- {
		r.logInfo(redisCluster.NamespacedName(), "Moving slots away from partition", "partition", partition, "node", node.ClusterNode.Name())
		err = clusterNodes.RebalanceCluster(ctx, map[string]int{
			node.ClusterNode.Name(): 0,
		}, kubernetes.MoveSlotOption{
			PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
		}, logger)
		if err != nil {
			if retry >= 0 {
				r.logInfo(redisCluster.NamespacedName(), "Retrying moving remaining slots...")
			}
		} else {
			break
		}
	}
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not move slots away from node")
		return err
	}

	// We need to give the cluster a chance to reach quorum, after all the slots have been moved.
	// We might also need to increase this time later,
	// to give all the Redis Clients a chance to update their internal maps.
	time.Sleep(time.Second * 5)

	// If we are running in Ephemeral mode, we need to forget the node before we restart it, to keep the cluster steady
	if redisCluster.Spec.Ephemeral {
		err = clusterNodes.ForgetNode(node.ClusterNode.Name())
		if err != nil {
			return err
		}
	}

	// RollingUpdate
	r.logInfo(redisCluster.NamespacedName(), "Executing partition Rolling Update", "partition", partition)
	localPartition := int32(partition)
	existingStateFulSet.Spec.UpdateStrategy = v1.StatefulSetUpdateStrategy{
		Type:          v1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &v1.RollingUpdateStatefulSetStrategy{Partition: &localPartition},
	}
	existingStateFulSet, err = r.updateStatefulSet(ctx, existingStateFulSet, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not update partition for Statefulset")
		return err
	}

	// Let's wait for the StatefulSet to start recreating the pod before we check for it's readiness
	// If we check it to early, we will likely see all available pods, and continue, and straight after that,
	// the new pod will join and mess with our whole process.
	time.Sleep(time.Second * 5)

	podReadyWaiter := utils.PodReadyWait{
		Client: r.Client,
	}
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel: redisCluster.Name,
				kubernetes.GetStatefulSetSelectorLabel(ctx, r.Client, redisCluster): "redis",
			},
		),
	}
	r.logInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStateFulSet.Spec.Replicas)))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(*(existingStateFulSet.Spec.Replicas)))
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not wait for Pods to become ready")
		return err
	}

	// We want to wait again after the pods become ready,
	// as the Redis Cluster needs to register that the node has joined
	time.Sleep(time.Second * 5)

	clusterNodes, err = r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	node, err = clusterNodes.GetNodeByPodName(fmt.Sprintf("%s-%d", existingStateFulSet.Name, partition))
	if err != nil {
		return err
	}

	// Next we want to make sure that the node which has joined is a master node.
	// If not, we need to reset the node and ClusterMeet it again,
	// to make sure it joins as a master.
	// This is very important, as the Operator is built around master only at the moment.
	info, err := node.ClusterNode.Call("INFO").Text()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not get info from Node")
	}
	if !strings.Contains(info, "role:master") {
		// The node restarted without being a master node.
		err = node.ClusterNode.Call("cluster", "reset").Err()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not reset node")
			return err
		}
	}

	// Due to an existing bug where a restarting node does not update it's own IP,
	// and incorrectly advertises the wrong data,
	// we need to rerun the cluster meet
	err = clusterNodes.ClusterMeet(ctx)
	if err != nil {
		return err
	}

	// After running the cluster meet, we want to wait again
	// to give the cluster a chance to settle, and clients to update with the new node,
	// before moving onto the next node in the set.
	time.Sleep(time.Second * 10)

	return nil
}

func (r *RedisClusterReconciler) UpgradePartitionWithReplicas(ctx context.Context, redisCluster *redisv1.RedisCluster, existingStateFulSet *v1.StatefulSet, partition int, logger logr.Logger) error {
	r.logInfo(redisCluster.NamespacedName(), "Looping partitions", "partition", partition)
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	node, err := clusterNodes.GetNodeByPodName(fmt.Sprintf("%s-%d", existingStateFulSet.Name, partition))
	if err != nil {
		return err
	}

	masterId := node.ClusterNode.Name()
	if node.ClusterNode.Replicate() != "" {
		masterId = node.ClusterNode.Replicate()
	}

	if node.IsMaster() {
		var replicaId string
		// We need to fail over to the replica
		for _, friendNode := range clusterNodes.Nodes {
			if node.ClusterNode.Name() == friendNode.ClusterNode.Replicate() {
				replicaId = friendNode.ClusterNode.Name()
				break
			}
		}
		if replicaId == "" {
			// No suitable replica has been found. We need to exit at this point.
			// We might want to rebalance at this point as a backup method.
			return errors.New("cannot find suitable replica to failover to")
		}

		masterId = replicaId
		replicaNode, err := clusterNodes.GetNodeByID(replicaId)
		if err != nil {
			return err
		}

		err = replicaNode.Call("CLUSTER", "FAILOVER").Err()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not failover replica node")
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Waiting for node to failover.")
		// We need to give the cluster a chance to reach quorum, after all the slots have been moved.
		// We might also need to increase this time later,
		// to give all the Redis Clients a chance to update their internal maps.
		time.Sleep(time.Second * 10)
		r.logInfo(redisCluster.NamespacedName(), "Done Waiting for node to failover.")
	}

	// If we are running in Ephemeral mode, we need to forget the node before we restart it, to keep the cluster steady
	if redisCluster.Spec.Ephemeral {
		r.logInfo(redisCluster.NamespacedName(), "Forgetting previous node")
		err = clusterNodes.ForgetNode(node.ClusterNode.Name())
		if err != nil {
			return err
		}
	}

	// RollingUpdate
	localPartition := int32(partition)
	existingStateFulSet.Spec.UpdateStrategy = v1.StatefulSetUpdateStrategy{
		Type:          v1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &v1.RollingUpdateStatefulSetStrategy{Partition: &localPartition},
	}
	existingStateFulSet, err = r.updateStatefulSet(ctx, existingStateFulSet, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not update partition for Statefulset")
		return err
	}

	// Let's wait for the StatefulSet to start recreating the pod before we check for it's readiness
	// If we check it to early, we will likely see all available pods, and continue, and straight after that,
	// the new pod will join and mess with our whole process.
	time.Sleep(time.Second * 5)

	podReadyWaiter := utils.PodReadyWait{
		Client: r.Client,
	}
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel: redisCluster.Name,
				kubernetes.GetStatefulSetSelectorLabel(ctx, r.Client, redisCluster): "redis",
			},
		),
	}
	r.logInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStateFulSet.Spec.Replicas)))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(*(existingStateFulSet.Spec.Replicas)))
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not wait for Pods to become ready")
		return err
	}

	// We want to wait again after the pods become ready,
	// as the Redis Cluster needs to register that the node has joined
	time.Sleep(time.Second * 5)

	r.logInfo(redisCluster.NamespacedName(), "Fetching new clusterNodes")
	clusterNodes, err = r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	node, err = clusterNodes.GetNodeByPodName(fmt.Sprintf("%s-%d", existingStateFulSet.Name, partition))
	if err != nil {
		return err
	}

	r.logInfo(redisCluster.NamespacedName(), "Meeting Cluster Friends")
	// Due to an existing bug where a restarting node does not update it's own IP,
	// and incorrectly advertises the wrong data,
	// we need to rerun the cluster meet
	err = clusterNodes.ClusterMeet(ctx)
	if err != nil {
		return err
	}

	// We want to wait again to ensure the cluster EPOCH is distributed after the meet
	time.Sleep(time.Second * 5)

	r.logInfo(redisCluster.NamespacedName(), "Replicating previous master")
	// Next we want to make sure that the node which has joined is a master node.
	// If not, we need to reset the node and ClusterMeet it again,
	// to make sure it joins as a master.
	// This is very important, as the Operator is built around master only at the moment.
	_, err = node.ClusterNode.ClusterReplicateWithNodeID(masterId)
	if err != nil {
		return err
	}

	// TODO: We need to wait for the sync to complete before we continue with the upgrade
	time.Sleep(time.Second * 10)

	return nil
}

func (r *RedisClusterReconciler) scaleCluster(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	var err error

	// By introducing master-replica cluster, the replicas returned by statefulset (which includes replica nodes) and
	// redisCluster's replicas (which is just masters) may not match if replicasPerMaster != 0
	realExpectedReplicas := int32(redisCluster.NodesNeeded())
	r.logInfo(redisCluster.NamespacedName(), "Expected cluster nodes", "Master nodes", redisCluster.Spec.Replicas, "Total nodes (including replicas)", realExpectedReplicas)

	sset, sset_err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if sset_err != nil {
		return true, err
	}
	currSsetReplicas := *(sset.Spec.Replicas)

	if realExpectedReplicas == currSsetReplicas {
		// Complete scaling
		if redisCluster.Status.Status == redisv1.StatusScalingUp {
			immediateRequeue, err := r.completeClusterScaleUp(ctx, redisCluster)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		} else {
			immediateRequeue, err := r.completeClusterScaleDown(ctx, redisCluster)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		}
	} else if realExpectedReplicas < currSsetReplicas {
		// Scaling down
		if redisCluster.Status.Substatus.Status == "" {
			err := r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingRobin, 0)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}
		}
		immediateRequeue, err := r.scaleDownCluster(ctx, redisCluster)
		if err != nil || immediateRequeue {
			return true, err
		}
	} else {
		// Scaling up
		if redisCluster.Status.Substatus.Status == "" {
			err := r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingPods, 0)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}
		}
		err = r.scaleUpCluster(ctx, redisCluster, realExpectedReplicas)
		if err != nil {
			return true, err
		}
	}

	// PodDisruptionBudget update
	// We use currSsetReplicas to check the configured number of replicas (not taking int account the extra pod if created)
	if redisCluster.Spec.Pdb.Enabled && math.Min(float64(redisCluster.Spec.Replicas), float64(currSsetReplicas)) > 1 {
		err = r.updatePodDisruptionBudget(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "ScaleCluster - Failed to update PodDisruptionBudget")
		} else {
			r.logInfo(redisCluster.NamespacedName(), "ScaleCluster - PodDisruptionBudget updated ", "Name", redisCluster.Name+"-pdb")
		}
	}

	return false, nil
}

func (r *RedisClusterReconciler) scaleDownCluster(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {

	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	robinRedis, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	// Update replicas
	replicas, replicasPerMaster, err := robinRedis.GetReplicas()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin replicas")
		return true, err
	}
	if replicas != int(redisCluster.Spec.Replicas) || replicasPerMaster != int(redisCluster.Spec.ReplicasPerMaster) {
		err = robinRedis.SetReplicas(int(redisCluster.Spec.Replicas), int(redisCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating Robin replicas", "replicas", redisCluster.Spec.Replicas,
				"replicas per master", redisCluster.Spec.ReplicasPerMaster)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, err
		}
		r.logInfo(redisCluster.NamespacedName(), "Robin replicas updated", "replicas before", replicas, "replicas per master before", replicasPerMaster,
			"replicas after", redisCluster.Spec.Replicas, "replicas per master after", redisCluster.Spec.ReplicasPerMaster)
	}

	// Check redis cluster replicas meet the requirements
	clusterNodes, err := robinRedis.GetClusterNodes()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin nodes info")
		return true, err
	}
	existingMasterNodes := len(clusterNodes.GetMasterNodes())
	existingReplicaNodes := len(clusterNodes.GetReplicaNodes())
	expectedMasterNodes := int(redisCluster.Spec.Replicas)
	expectedRelicaNodes := int(redisCluster.Spec.Replicas * redisCluster.Spec.ReplicasPerMaster)
	if existingMasterNodes != expectedMasterNodes || existingReplicaNodes != expectedRelicaNodes {
		r.logInfo(redisCluster.NamespacedName(), "Waiting for Robin to scale the cluster", "expected master nodes", expectedMasterNodes,
			"existing master nodes", existingMasterNodes, "expected replica nodes", expectedRelicaNodes,
			"existing replica nodes", existingReplicaNodes)
		return true, nil // Requeue
	}

	// Update StatefulSet
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		return true, err
	}
	sset.Spec.Replicas = &redisCluster.Spec.Replicas
	_, err = r.updateStatefulSet(ctx, sset, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		return true, err
	}
	err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingPods, 0)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
		return true, err
	}

	return false, nil
}

func (r *RedisClusterReconciler) completeClusterScaleDown(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	switch redisCluster.Status.Substatus.Status {
	case redisv1.SubstatusScalingPods:
		// StatefulSet has been updated with new replicas/replicasPerMaster at this point.
		// We will ensure all pods are Ready and, then, update the new replicas in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready to end Scaling down", "expected nodes", redisCluster.NodesNeeded())
			return true, nil
		}

		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingFinalizing, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		if redisCluster.Spec.DeletePVC && !redisCluster.Spec.Ephemeral {
			culledNodes, err := r.getCulledNodes(ctx, redisCluster)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error getting culled nodes")
				return true, err
			}

			for _, node := range culledNodes {
				if err := r.deleteNodePVC(ctx, redisCluster, &node); err != nil {
					r.logError(redisCluster.NamespacedName(), err, fmt.Sprintf("Could not handle PVC for node: %s", node.Name))
					return true, err
				}
			}
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redisv1.SubstatusScalingFinalizing:
		// Final step: ensure the cluster is Ok.

		logger := r.getHelperLogger((redisCluster.NamespacedName()))
		robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
			return true, err
		}
		check, errors, warnings, err := robin.ClusterCheck()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			// Cluster scaling not completed -> requeue
			r.logInfo(redisCluster.NamespacedName(), "ScaleCluster - Waiting for Redis cluster readiness before ending the cluster scaling", "errors", errors, "warnings", warnings)
			return true, nil
		}

	default:
		r.logError(redisCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redisCluster.Status.Substatus.Status)
		return true, nil
	}

	// Cluster scaling completed!
	return false, nil
}

func (r *RedisClusterReconciler) getCulledNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) ([]robin.Node, error) {
	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	redisRobin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return nil, err
	}
	clusterNodes, err := redisRobin.GetClusterNodes()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting cluster nodes info from Robin")
		return nil, err
	}

	podPrefix := fmt.Sprintf("%s-", redisCluster.Name)
	var culledNodes []robin.Node
	for _, node := range clusterNodes.Nodes {
		ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.Name, podPrefix))
		if ordinal >= int(redisCluster.Spec.Replicas) {
			culledNodes = append(culledNodes, node)
		}
	}

	return culledNodes, nil
}

func (r *RedisClusterReconciler) deleteNodePVC(ctx context.Context, redisCluster *redisv1.RedisCluster, node *robin.Node) error {
	pvc, err := r.getPersistentVolumeClaim(ctx, r.Client, redisCluster, fmt.Sprintf("data-%s", node.Name))
	if errors2.IsNotFound(err) {
		r.logError(redisCluster.NamespacedName(), err, fmt.Sprintf("PVC of node %s doesn't exist. Skipping.", node.Name))
		return nil
	} else if err != nil {
		return fmt.Errorf("could not get PVC of node %s: %w", node.Name, err)
	}

	r.logInfo(redisCluster.NamespacedName(), fmt.Sprintf("Deleting PVC: data-%s", node.Name))
	return r.deletePVC(ctx, r.Client, pvc)
}

func (r *RedisClusterReconciler) scaleUpCluster(ctx context.Context, redisCluster *redisv1.RedisCluster, realExpectedReplicas int32) error {
	r.logInfo(redisCluster.NamespacedName(), "ScaleCluster - updating statefulset replicas", "newsize", realExpectedReplicas)

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		return err
	}

	sset.Spec.Replicas = &realExpectedReplicas
	_, err = r.updateStatefulSet(ctx, sset, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		return err
	}

	return nil
}

func (r *RedisClusterReconciler) completeClusterScaleUp(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	robinRedis, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	switch redisCluster.Status.Substatus.Status {
	case redisv1.SubstatusScalingPods:
		// StatefulSet has been updated with new replicas/replicasPerMaster at this point.
		// We will ensure all pods are Ready and, then, update the new replicas in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready to end Scaling up", "expected nodes", redisCluster.NodesNeeded())
			return true, nil
		}

		// Update Robin with new replicas/replicasPerMaster
		err = robinRedis.SetReplicas(int(redisCluster.Spec.Replicas), int(redisCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating replicas in Robin", "replicas", redisCluster.Spec.Replicas, "replicasPerMaster", redisCluster.Spec.ReplicasPerMaster)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, err
		}

		// Update the status to continue completing the cluster scaling
		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingRobin, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redisv1.SubstatusScalingRobin:
		// Robin was already updated with new replicas/replicasPerMaster.
		// We will ensure that all cluster nodes are initialized before asking Robin to meet all new nodes,
		// forget outdated nodes, ensure slots coverage and rebalance.

		clusterNodes, err := robinRedis.GetClusterNodes()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
			return true, err
		}
		masterNodes := clusterNodes.GetMasterNodes()
		if len(masterNodes) != int(redisCluster.Spec.Replicas) {
			r.logInfo(redisCluster.NamespacedName(), "ScaleCluster - Inconsistency. Statefulset replicas equals to RedisCluster replicas but we have a different number of cluster nodes. Trying to fix it...",
				"RedisCluster replicas", redisCluster.Spec.Replicas, "Cluster nodes", masterNodes)
		}
		err = robinRedis.ClusterFix()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error asking to Robin to ensure the cluster is ok")
			return true, err
		}
		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusScalingFinalizing, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redisv1.SubstatusScalingFinalizing:
		// Final step: ensure the cluster is Ok.

		check, errors, warnings, err := robinRedis.ClusterCheck()
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			// Cluster scaling not completed -> requeue
			r.logInfo(redisCluster.NamespacedName(), "ScaleCluster - Waiting for Redis cluster readiness before ending the cluster scaling", "errors", errors, "warnings", warnings)
			return true, nil
		}

	default:
		r.logError(redisCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redisCluster.Status.Substatus.Status)
		return true, nil
	}

	// Cluster scaling completed!
	return false, nil
}

func (r *RedisClusterReconciler) isOwnedByUs(o client.Object) bool {
	labels := o.GetLabels()
	if _, found := labels[redis.RedisClusterLabel]; found {
		return true
	}
	return false
}

func (r *RedisClusterReconciler) GetRedisClient(ctx context.Context, ip string, secret string) *redisclient.Client {
	redisclient.NewClusterClient(&redisclient.ClusterOptions{})
	rdb := redisclient.NewClient(&redisclient.Options{
		Addr:     fmt.Sprintf("%s:%d", ip, redis.RedisCommPort),
		Password: secret,
		DB:       0,
	})
	return rdb
}

func (r *RedisClusterReconciler) GetReadyNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (map[string]*redisv1.RedisNode, error) {
	return r.GetReadyNodesFunc(ctx, redisCluster)
}

func (r *RedisClusterReconciler) DoGetReadyNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (map[string]*redisv1.RedisNode, error) {
	allPods := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel:                     redisCluster.GetName(),
			r.getStatefulSetSelectorLabel(redisCluster): "redis",
		},
	)

	err := r.Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, errors.New("could not list pods")
	}
	readyNodes := make(map[string]*redisv1.RedisNode, 0)
	clusterNodes := make(map[string]redisv1.RedisNode, 0)
	redisSecret, _ := r.GetRedisSecret(redisCluster)
	for _, pod := range allPods.Items {
		for _, s := range pod.Status.Conditions {
			if s.Type == corev1.PodReady && s.Status == corev1.ConditionTrue {
				// get node id
				redisClient := r.GetRedisClient(ctx, pod.Status.PodIP, redisSecret)
				defer redisClient.Close()
				nodeId, err := redisClient.Do(ctx, "cluster", "myid").Result()
				if err != nil {
					r.logInfo(redisCluster.NamespacedName(), "Could not fetch node id", "pod: ", pod.Status.PodIP)
				}
				if nodeId == nil {
					return nil, errors.New("can't fetch node id")
				}
				// Get cluster nodes info if not already fetched
				if len(clusterNodes) == 0 {
					nodes, err := redisClient.Do(ctx, "cluster", "nodes").Result()
					if err != nil {
						return nil, errors.New("can't fetch cluster nodes")
					}
					clusterNodes = redis.ParseClusterNodes(nodes.(string))
				}

				readyNodes[nodeId.(string)] = &redisv1.RedisNode{IP: pod.Status.PodIP, Name: pod.GetName(), IsMaster: true, ReplicaOf: ""}

				if value, ok := clusterNodes[nodeId.(string)]; ok {
					readyNodes[nodeId.(string)].IsMaster = value.IsMaster
					readyNodes[nodeId.(string)].ReplicaOf = value.ReplicaOf
				}
			}
		}
	}
	r.logInfo(redisCluster.NamespacedName(), "GetReadyNodes", "nodes", readyNodes, "numReadyNodes", strconv.Itoa(len(readyNodes)))
	return readyNodes, nil
}

func (r *RedisClusterReconciler) GetRedisSecret(redisCluster *redisv1.RedisCluster) (string, error) {
	if redisCluster.Spec.Auth.SecretName == "" {
		return "", nil
	}

	secret := &corev1.Secret{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: redisCluster.Spec.Auth.SecretName, Namespace: redisCluster.Namespace}, secret)
	if err != nil {
		return "", err
	}
	redisSecret := string(secret.Data["requirepass"])
	return redisSecret, nil
}

func makeRange(min int, max int) []int {
	a := make([]int, max-min+1)
	for i := range a {
		a[i] = min + i
	}
	return a
}

func calculateRCConfigChecksum(redisCluster *redisv1.RedisCluster) string {
	hash := md5.Sum([]byte(redisCluster.Spec.Config))
	return hex.EncodeToString(hash[:])
}

// Updates StatefulSet annotations with the config checksum (annotation "config-checksum")
func (r *RedisClusterReconciler) addConfigChecksumAnnotation(statefulSet *v1.StatefulSet, redisCluster *redisv1.RedisCluster) *v1.StatefulSet {
	var updatedAnnotations map[string]string

	checksum := calculateRCConfigChecksum(redisCluster)

	if statefulSet.Spec.Template.Annotations == nil {
		updatedAnnotations = make(map[string]string)
	} else {
		updatedAnnotations = statefulSet.Spec.Template.Annotations
	}

	updatedAnnotations[ConfigChecksumAnnotation] = checksum
	statefulSet.Spec.Template.Annotations = updatedAnnotations

	return statefulSet
}

// Checks if the configuration has changed.
// If the annotation StatefulSet.Spec.Template.Annotations[ConfigChecksumAnnotation]  exists, we check first if the
// configuration checksum has changed.
// If not, we compare the configuration properties.
func (r *RedisClusterReconciler) isConfigChanged(redisCluster *redisv1.RedisCluster, statefulSet *v1.StatefulSet,
	configMap *corev1.ConfigMap) (bool, string) {
	// Comparing config checksums
	if statefulSet.Spec.Template.Annotations != nil {
		checksum, exists := statefulSet.Spec.Template.Annotations[ConfigChecksumAnnotation]
		if exists {
			calculatedChecksum := calculateRCConfigChecksum(redisCluster)
			if checksum != calculatedChecksum {
				r.logInfo(redisCluster.NamespacedName(), "Config checksum changed", "existing checksum", checksum, "calculated checksum", calculatedChecksum)
				return true, "Redis Config Changed - Checksum"
			}
			return false, ""
		}
	}
	// Comparing properties
	desiredConfig := redis.MergeWithDefaultConfig(
		redis.ConfigStringToMap(redisCluster.Spec.Config),
		redisCluster.Spec.Ephemeral,
		redisCluster.Spec.ReplicasPerMaster)
	observedConfig := redis.ConfigStringToMap(configMap.Data["redis.conf"])
	if !reflect.DeepEqual(observedConfig, desiredConfig) {
		return true, "Redis Config Changed - properties"
	}
	return false, ""
}

func (r *RedisClusterReconciler) getClusterNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (kubernetes.ClusterNodeList, error) {
	clusterNodes, err := kubernetes.GetKubernetesClusterNodes(ctx, r.Client, redisCluster)
	if err != nil {
		return kubernetes.ClusterNodeList{}, err
	}
	err = clusterNodes.LoadInfoForNodes()
	if err != nil {
		return kubernetes.ClusterNodeList{}, err
	}
	return clusterNodes, nil
}

func (r *RedisClusterReconciler) freeClusterNodes(clusterNodes kubernetes.ClusterNodeList, RCNamespacedName types.NamespacedName) {
	for i := range clusterNodes.Nodes {
		err := kubernetes.FreeKubernetesClusterNode(clusterNodes.Nodes[i])
		if err != nil {
			// Log error and keep trying with the other nodes
			r.logError(RCNamespacedName, err, "Error releasing cluster node")
		}
	}
}

func (r *RedisClusterReconciler) scaleUpForUpgrade(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	// Add a new node to the cluster to make sure that there's enough space to move slots
	// But first lets check if there is a pod dangling from a previous attempt that gone sour
	// For example if a non-existant redis image is requested, it'd get stuck on n+1th pod being never created successfuly and that pod
	// might be still there.

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error while getting StatefulSet")
		return false, err
	}

	originalCount := redisCluster.Spec.Replicas
	if *sset.Spec.Replicas == originalCount && redisCluster.Status.Substatus.Status == "" {
		redisCluster.Spec.Replicas++
		_, err = r.scaleCluster(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when scaling up")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
			return false, err
		}
		err = r.updateClusterSubStatus(ctx, redisCluster, redisv1.SubstatusUpgradingScaleUp, 0)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return false, err
		}
	} else if *sset.Spec.Replicas == originalCount+1 {
		// Resume if the cluster was already scaled for upgrading.
		r.logInfo(redisCluster.NamespacedName(), "Cluster already scaled, resume the processing")
		redisCluster.Spec.Replicas = *sset.Spec.Replicas
	}

	podsReady, err := r.allPodsReady(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not check for pods readiness")
		return false, err
	}
	if !podsReady {
		// pods not ready yet, return to requeue and keep waiting
		return false, nil
	}

	return true, nil
}
