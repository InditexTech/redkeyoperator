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
	"sort"
	"strconv"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	redis "github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/utils"

	"github.com/go-logr/logr"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/intstr"

	redisclient "github.com/redis/go-redis/v9"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CONFIG_CHECKSUM_ANNOTATION = "inditex.com/redis-conf"
)

type RedisClient struct {
	NodeId      string
	RedisClient *redisclient.Client
	IP          string
}

var redisClients map[string]*RedisClient = make(map[string]*RedisClient)

func (r *RedisClusterReconciler) RefreshRedisClients(ctx context.Context, redisCluster *redisv1.RedisCluster) {
	nodes, _ := r.GetReadyNodes(ctx, redisCluster)
	for nodeId, node := range nodes {
		secret, _ := r.GetRedisSecret(redisCluster)
		redisClient := r.GetRedisClient(ctx, node.IP, secret)
		err := redisClient.Ping(ctx).Err()
		redisClient.Close()
		if err != nil {
			r.LogInfo(redisCluster.NamespacedName(), "RefreshRedisClients - Redis client for node is errorring", "node", nodeId, "error", err)
			redisClients[nodeId] = nil
		}
	}
}

func (r *RedisClusterReconciler) GetRedisClientForNode(ctx context.Context, nodeId string, redisCluster *redisv1.RedisCluster) (*redisclient.Client, error) {
	nodes, _ := r.GetReadyNodes(ctx, redisCluster)
	// If redisClient for this node has not been initialized, or the IP has changed
	if nodes[nodeId] == nil {
		return nil, fmt.Errorf("node %s does not exist", nodeId)
	}
	if redisClients[nodeId] == nil || redisClients[nodeId].IP != nodes[nodeId].IP {
		secret, _ := r.GetRedisSecret(redisCluster)
		rdb := r.GetRedisClient(ctx, nodes[nodeId].IP, secret)
		redisClients[nodeId] = &RedisClient{NodeId: nodeId, RedisClient: rdb, IP: nodes[nodeId].IP}
	}

	return redisClients[nodeId].RedisClient, nil
}

func (r *RedisClusterReconciler) RemoveRedisClientForNode(nodeId string, ctx context.Context, redisCluster *redisv1.RedisCluster) {
	if redisClients[nodeId] == nil {
		return
	}
	redisClients[nodeId].RedisClient.Close()
	redisClients[nodeId] = nil
}

func (r *RedisClusterReconciler) ConfigureRedisCluster(ctx context.Context, redisCluster *redisv1.RedisCluster, clusterNodes kubernetes.ClusterNodeList) error {
	r.LogInfo(redisCluster.NamespacedName(), "ConfigureRedisCluster", "readyNodes", clusterNodes.SimpleNodesObject())
	err := clusterNodes.LoadInfoForNodes()
	if err != nil {
		return err
	}
	err = clusterNodes.ClusterMeet(ctx)
	if err != nil {
		r.Recorder.Event(redisCluster, "Warning", "ClusterMeet", "Error when attempting ClusterMeet")
		return err
	}

	podsReady, err := r.AllPodsReady(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check ready pods")
	}
	if redisCluster.Spec.Ephemeral && podsReady {
		logger := r.GetHelperLogger(redisCluster.NamespacedName())
		err := clusterNodes.RemoveClusterOutdatedNodes(ctx, redisCluster, logger)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not check outdated nodes")
		}
	}

	if podsReady {
		logger := r.GetHelperLogger(redisCluster.NamespacedName())
		err := clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
		if err != nil {
			return err
		}

		err = clusterNodes.AssignMissingSlots()
		if err != nil {
			r.Recorder.Event(redisCluster, "Warning", "SlotAssignment", "Error when attempting AssignSlots")
			return err
		}
	}

	return nil
}

func (r *RedisClusterReconciler) UpdateScalingStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	sset, ssetErr := r.FindExistingStatefulSetFunc(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if ssetErr != nil {
		return ssetErr
	}
	currSsetReplicas := *(sset.Spec.Replicas)
	realExpectedReplicas := int32(redisCluster.NodesNeeded())

	logger := r.GetHelperLogger(redisCluster.NamespacedName())

	if realExpectedReplicas < currSsetReplicas {
		redisCluster.Status.Status = redisv1.StatusScalingDown
		SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
		r.SetConditionTrue(redisCluster, redisv1.ConditionScalingDown, fmt.Sprintf("Scaling down from %d to %d nodes", currSsetReplicas, redisCluster.Spec.Replicas))
	}
	if realExpectedReplicas > currSsetReplicas {
		redisCluster.Status.Status = redisv1.StatusScalingUp
		r.SetConditionTrue(redisCluster, redisv1.ConditionScalingUp, fmt.Sprintf("Scaling up from %d to %d nodes", currSsetReplicas, redisCluster.Spec.Replicas))
		SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
	}
	if realExpectedReplicas == currSsetReplicas {
		if redisCluster.Status.Status == redisv1.StatusScalingDown {
			redisCluster.Status.Status = redisv1.StatusReady
			SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
		}
		if redisCluster.Status.Status == redisv1.StatusScalingUp {
			if len(redisCluster.Status.Nodes) == int(currSsetReplicas) {
				redisCluster.Status.Status = redisv1.StatusReady
				SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
			}
		}
	}

	// initialize scaling up and down conditions if they haven't been set yet
	c := meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusScalingDown)
	if c == nil {
		SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
	}

	c = meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusScalingUp)
	if c == nil {
		SetConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
	}

	return nil
}

func (r *RedisClusterReconciler) UpdateUpgradingStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	req := controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}}
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}

	// Handle changes in spec.override
	_, changed := r.OverrideStatefulSet(ctx, req, redisCluster, statefulSet)

	if changed {
		r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Override changed")
		redisCluster.Status.Status = redisv1.StatusUpgrading
		r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Override changed")
		return nil
	}

	ns := redisCluster.NamespacedName()

	configMap, err := r.FindExistingConfigMapFunc(ctx, controllerruntime.Request{NamespacedName: ns})
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(statefulSet.Spec.UpdateStrategy, v1.StatefulSetUpdateStrategy{}) {
		// An UpdateStrategy is already set.
		// We can reuse the partition as the starting partition
		// We should only do this for partitions higher than zero,
		// as we cannot unset the partition after an upgrade, and the default will be zero.
		//
		// So 0 partition will trigger a bad thing, where the whole cluster will be immediately applied,
		// and all pods destroyed simultaneously.
		// We need to only set the starting partition if it is higher than 0,
		// otherwise use the replica count one from before.
		if int(*(statefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)) > 0 {
			r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Previous upgrade not complete")
			redisCluster.Status.Status = redisv1.StatusUpgrading
			return nil
		}
	}

	if redisCluster.Spec.Labels != nil {
		// get the actual value of labels configured in the redisCluster object
		desiredLabels := make(map[string]string)
		maps.Copy(desiredLabels, *redisCluster.Spec.Labels)

		// Add labels from override
		if redisCluster.Spec.Override != nil && redisCluster.Spec.Override.StatefulSet != nil && redisCluster.Spec.Override.StatefulSet.Spec.Template.Labels != nil {
			for k2, v2 := range redisCluster.Spec.Override.StatefulSet.Spec.Template.Labels {
				desiredLabels[k2] = v2
			}
		}

		r.LogInfo(redisCluster.NamespacedName(), "Expected real labels configured in RedisCluster object", "Spec.Labels", desiredLabels)

		// get the current value of labels configured in the statefulset object
		observedLabels := statefulSet.Spec.Template.Labels
		r.LogInfo(redisCluster.NamespacedName(), "Current statefulset configuration", "Spec.Template.Labels", observedLabels)

		// Validate if were added new labels
		for key, value := range desiredLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			val, exists := statefulSet.Spec.Template.Labels[key]
			if !exists {
				r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusUpgrading
				r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			} else {
				if value != val {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
					redisCluster.Status.Status = redisv1.StatusUpgrading
					r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
					return nil
				}
			}
		}

		defaultLabels := map[string]string{
			redis.RedisClusterLabel:                     redisCluster.Name,
			r.GetStatefulSetSelectorLabel(redisCluster): "redis",
		}

		// add defaultLabels
		maps.Copy(desiredLabels, defaultLabels)

		// Validate if  were deleted existing labels
		for key := range observedLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			_, exists := desiredLabels[key]

			if !exists {
				r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusUpgrading
				r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			}
		}
	}

	// Check whether the redis configuration has changed,
	// as this will constitute a cluster upgrade
	configChanged, reason := r.isConfigChanged(redisCluster, statefulSet, configMap)
	if configChanged {
		r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", reason)
		redisCluster.Status.Status = redisv1.StatusUpgrading
		r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Configuration in redis.conf is being updated")
		return nil
	}

	if redisCluster.Spec.Resources != nil {
		// Check whether any upgradeable object changes have been made.
		// Any change in these attributes or properties will constitute a live upgrade.
		desiredResources := *(redisCluster.Spec.Resources)
		observedResources := statefulSet.Spec.Template.Spec.Containers[0].Resources

		// We need to individually compare the resources we care about,
		// as DeepEqual gets it wrong for CPU quantities
		if !reflect.DeepEqual(observedResources.Limits.Cpu().String(), desiredResources.Limits.Cpu().String()) ||
			!reflect.DeepEqual(observedResources.Limits.Memory().String(), desiredResources.Limits.Memory().String()) ||
			!reflect.DeepEqual(observedResources.Requests.Cpu().String(), desiredResources.Requests.Cpu().String()) ||
			!reflect.DeepEqual(observedResources.Requests.Memory().String(), desiredResources.Requests.Memory().String()) {
			r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Resource Requests & Limits Changed", "observed", observedResources, "desired", desiredResources)

			redisCluster.Status.Status = redisv1.StatusUpgrading

			r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Resource Requests & Limits Changed")

			return nil
		}
	}

	// Check whether any upgradeable object changes have been made.
	// Any change in these attributes or properties will constitute a live upgrade.
	desiredImage := redisCluster.Spec.Image
	observedImage := statefulSet.Spec.Template.Spec.Containers[0].Image
	if observedImage != desiredImage {
		r.LogInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Image Changed", "observed", observedImage, "desired", desiredImage)
		redisCluster.Status.Status = redisv1.StatusUpgrading

		r.SetConditionTrue(redisCluster, redisv1.ConditionUpgrading, fmt.Sprintf("Redis Image Changed: observed %s desired %s", observedImage, desiredImage))

		return nil
	}

	redisCluster.Status.Status = redisv1.StatusReady

	c := meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusUpgrading)
	if c != nil && c.Status == metav1.ConditionTrue {
		SetConditionFalse(r.GetHelperLogger(redisCluster.NamespacedName()), redisCluster, redisv1.ConditionUpgrading)
	}

	return nil
}

func (r *RedisClusterReconciler) CheckPDB(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// Check if the pdb availables are changed
	if redisCluster.Spec.Pdb.Enabled && redisCluster.Spec.Replicas > 1 {
		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to get PodDisruptionBudget")
		}
		if pdb != nil {
			maxUnavailableFromRedisCluster := &redisCluster.Spec.Pdb.PdbSizeUnavailable
			minAvailableFromRedisCluster := &redisCluster.Spec.Pdb.PdbSizeAvailable
			if redisCluster.Spec.Pdb.PdbSizeAvailable.IntVal == 0 && redisCluster.Spec.Pdb.PdbSizeAvailable.StrVal == "" {
				minAvailableFromRedisCluster = nil
			}
			if redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal == 0 && redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal == "" {
				maxUnavailableFromRedisCluster = nil
			}
			if maxUnavailableFromRedisCluster != nil {
				if pdb.Spec.MaxUnavailable != nil {
					if pdb.Spec.MaxUnavailable.IntVal != redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal || pdb.Spec.MaxUnavailable.StrVal != redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal {
						r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedisCluster)
						redisCluster.Status.Status = redisv1.StatusConfiguring
						r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
						return nil
					}
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PBD changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedisCluster)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}

			} else if minAvailableFromRedisCluster != nil {
				if pdb.Spec.MinAvailable != nil {
					if pdb.Spec.MinAvailable.IntVal != redisCluster.Spec.Pdb.PdbSizeAvailable.IntVal || pdb.Spec.MinAvailable.StrVal != redisCluster.Spec.Pdb.PdbSizeAvailable.StrVal {
						r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedisCluster)
						redisCluster.Status.Status = redisv1.StatusConfiguring
						r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
						return nil
					}
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedisCluster)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}
			}
			// Selector match labels check
			desiredLabels := map[string]string{redis.RedisClusterLabel: redisCluster.ObjectMeta.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"}
			if len(pdb.Spec.Selector.MatchLabels) != len(desiredLabels) {
				r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusConfiguring
				r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
				return nil
			}
			for k, v := range desiredLabels {
				if pdb.Spec.Selector.MatchLabels[k] != v {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}
			}
		}
	} else {
		// Delete PodDisruptionBudget if exists
		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err == nil && pdb != nil {
			pdb.Name = redisCluster.Name + "-pdb"
			pdb.Namespace = redisCluster.Namespace
			err := r.DeletePodDisruptionBudget(ctx, pdb, redisCluster)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
			} else {
				r.LogInfo(redisCluster.NamespacedName(), "PodDisruptionBudget Deleted")
			}
		}
	}
	return nil
}

func (r *RedisClusterReconciler) UpgradeCluster(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	req := controllerruntime.Request{
		NamespacedName: types.NamespacedName{
			Name:      redisCluster.Name,
			Namespace: redisCluster.Namespace,
		},
	}

	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}
	logger := r.GetHelperLogger(redisCluster.NamespacedName())

	// We need to ensure that the upgrade is really necessary,
	// and we are not just host to a double reconciliation attempt
	err = r.UpdateUpgradingStatus(ctx, redisCluster)
	if err != nil {
		return fmt.Errorf("could not update upgrading status: %w", err)
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

	// Fast upgrade: If purgeKeysOnRebalance property is set to 'true' and we have no replicas. No need to iterate over the partitions.
	// If fast upgrade is not allowed but we have no replicas, the cluster must be scaled up adding one extra
	// node to be able to move slots and keys in order to ensure keys are preserved.
	fastUpgrade := false
	scaledBeforeUpgrade := false
	if redisCluster.Spec.PurgeKeysOnRebalance && redisCluster.Spec.ReplicasPerMaster == 0 {
		r.LogInfo(redisCluster.NamespacedName(), "Fast upgrade will be performed")
		fastUpgrade = true
		err = r.updateSubStatus(ctx, redisCluster, redisv1.SubstatusFastUpgrade, 0)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else if !redisCluster.Spec.PurgeKeysOnRebalance && redisCluster.Spec.ReplicasPerMaster == 0 {
		r.LogInfo(redisCluster.NamespacedName(), "Scaling Up the cluster before upgrading")
		_, err = r.scaleUpAndWait(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when scaling up before upgrade")
			return err
		}
		scaledBeforeUpgrade = true
	}

	// Update labels, configuration, annotations, overrides, etc.
	existingStatefulSet, err = r.UpgradeClusterConfigurationUpdate(ctx, redisCluster)
	if err != nil {
		return err
	}

	// Before we proceed, we want to wait for all pods to be ready.
	// We might have just finished a scaling event or something of the likes,
	// and if we upgrade straight away, we will run into bugs where pods cannot be contacted for the upgrade
	podReadyWaiter := utils.PodReadyWait{
		Client: r.Client,
	}
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.GetStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}

	r.LogInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStatefulSet.Spec.Replicas)))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(*(existingStatefulSet.Spec.Replicas)))
	if err != nil {
		return err
	}

	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
	if err != nil {
		return err
	}

	if fastUpgrade {
		r.LogInfo(redisCluster.NamespacedName(), "Fast upgrade")

		// Rolling update
		err = r.FastUpgradeRollingUpdate(ctx, redisCluster, existingStatefulSet)
		if err != nil {
			return err
		}

		// Wait till all pods have been recreated.
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStatefulSet.Spec.Replicas)))
		err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 10*time.Minute, &listOptions, int(*(existingStatefulSet.Spec.Replicas)))
		if err != nil {
			return err
		}

		// Rebuild the cluster
		err = r.FastUpgradeRebuildCluster(ctx, redisCluster, logger)
		if err != nil {
			return err
		}
	} else {
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
			if int(*(existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)) > 0 {
				r.LogInfo(redisCluster.NamespacedName(), "Update Strategy already set. Reusing partition", "partition", existingStatefulSet.Spec.UpdateStrategy.RollingUpdate.Partition)
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
		r.LogInfo(redisCluster.NamespacedName(), "Scaling down after the upgrade")
		redisCluster.Spec.Replicas--
		err = r.ScaleCluster(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error scaling down after the upgrade")
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Scaling down after the upgrade completed")
	}

	// We want to sleep to make sure the k8s client
	// gets a chance to update the pod list in the cache before we try to rebalance again,
	// otherwise it gets an outdated set of IPs
	time.Sleep(5 * time.Second)

	clusterNodes, err = r.getClusterNodes(ctx, redisCluster)
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
	SetConditionFalse(logger, redisCluster, redisv1.ConditionUpgrading)

	return nil
}

func (r *RedisClusterReconciler) UpgradeClusterConfigurationUpdate(ctx context.Context, redisCluster *redisv1.RedisCluster) (*v1.StatefulSet, error) {
	req := controllerruntime.Request{
		NamespacedName: types.NamespacedName{
			Name:      redisCluster.Name,
			Namespace: redisCluster.Namespace,
		},
	}

	// RedisCluster .Spec.Labels
	mergedLabels := *redisCluster.Spec.Labels
	defaultLabels := map[string]string{
		redis.RedisClusterLabel:                     redisCluster.Name,
		r.GetStatefulSetSelectorLabel(redisCluster): "redis",
	}
	maps.Copy(mergedLabels, defaultLabels)

	// Get configmap
	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		return nil, err
	}
	configMap.Labels = mergedLabels

	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(redisCluster)
	r.LogInfo(redisCluster.NamespacedName(), "Updating configmap", "configmap", configMap.Name)
	// Update ConfigMap
	err = r.Client.Update(ctx, configMap)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when creating configmap")
		return nil, err
	}

	// Get Service
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		return nil, err
	}
	// Update the Service labels
	maps.Copy(service.Labels, mergedLabels)
	r.LogInfo(redisCluster.NamespacedName(), "Updating service", "service", service.Name)
	err = r.Client.Update(ctx, service)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when updating service")
	}

	// Get existing StateFulSet
	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return nil, err
	}
	r.LogInfo(redisCluster.NamespacedName(), "Updating statefulset", "StateFulSet", existingStatefulSet.Name)
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
	existingStatefulSet, _ = r.OverrideStatefulSet(ctx, req, redisCluster, existingStatefulSet)

	return existingStatefulSet, nil
}

func (r *RedisClusterReconciler) FastUpgradeRollingUpdate(ctx context.Context, redisCluster *redisv1.RedisCluster, existingStatefulSet *v1.StatefulSet) error {
	localPartition := int32(0)
	maxUnavailable := intstr.FromString("100%")
	existingStatefulSet.Spec.UpdateStrategy = v1.StatefulSetUpdateStrategy{
		Type:          v1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &v1.RollingUpdateStatefulSetStrategy{Partition: &localPartition, MaxUnavailable: &maxUnavailable},
	}
	_, err := r.UpdateStatefulSet(ctx, existingStatefulSet, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not update partition for Statefulset")
		return err
	}
	return nil
}

func (r *RedisClusterReconciler) FastUpgradeRebuildCluster(ctx context.Context, redisCluster *redisv1.RedisCluster, logger logr.Logger) error {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	err = clusterNodes.ClusterMeet(ctx)
	if err != nil {
		return err
	}

	err = clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
	if err != nil {
		return err
	}

	// We want to check if any slots are missing
	err = r.AssignMissingSlots(ctx, redisCluster)
	if err != nil {
		return err
	}

	err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
	if err != nil {
		return err
	}

	return nil
}

func (r *RedisClusterReconciler) UpgradePartition(ctx context.Context, redisCluster *redisv1.RedisCluster, existingStateFulSet *v1.StatefulSet, partition int, logger logr.Logger) error {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	r.LogInfo(redisCluster.NamespacedName(), "Looping partitions for Rolling Update", "partition", partition)
	node, err := clusterNodes.GetNodeByPodName(fmt.Sprintf("%s-%d", existingStateFulSet.Name, partition))
	if err != nil {
		return err
	}

	// Empty the node by moving its slots to another node.
	// If any slot cannot be moved, it will be retried.
	for retry := 3; retry >= 0; retry-- {
		r.LogInfo(redisCluster.NamespacedName(), "Moving slots away from partition", "partition", partition, "node", node.ClusterNode.Name())
		err = clusterNodes.RebalanceCluster(ctx, map[string]int{
			node.ClusterNode.Name(): 0,
		}, kubernetes.MoveSlotOption{
			PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
		}, logger)
		if err != nil {
			if retry >= 0 {
				r.LogInfo(redisCluster.NamespacedName(), "Retrying moving remaining slots...")
			}
		} else {
			break
		}
	}
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not move slots away from node")
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
	r.LogInfo(redisCluster.NamespacedName(), "Executing partition Rolling Update", "partition", partition)
	localPartition := int32(partition)
	existingStateFulSet.Spec.UpdateStrategy = v1.StatefulSetUpdateStrategy{
		Type:          v1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &v1.RollingUpdateStatefulSetStrategy{Partition: &localPartition},
	}
	existingStateFulSet, err = r.UpdateStatefulSet(ctx, existingStateFulSet, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not update partition for Statefulset")
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
	r.LogInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStateFulSet.Spec.Replicas)))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(*(existingStateFulSet.Spec.Replicas)))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not wait for Pods to become ready")
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
		r.LogError(redisCluster.NamespacedName(), err, "Could not get info from Node")
	}
	if !strings.Contains(info, "role:master") {
		// The node restarted without being a master node.
		err = node.ClusterNode.Call("cluster", "reset").Err()
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not reset node")
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
	r.LogInfo(redisCluster.NamespacedName(), "Looping partitions", "partition", partition)
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
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
			r.LogError(redisCluster.NamespacedName(), err, "Could not failover replica node")
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for node to failover.")
		// We need to give the cluster a chance to reach quorum, after all the slots have been moved.
		// We might also need to increase this time later,
		// to give all the Redis Clients a chance to update their internal maps.
		time.Sleep(time.Second * 10)
		r.LogInfo(redisCluster.NamespacedName(), "Done Waiting for node to failover.")
	}

	// If we are running in Ephemeral mode, we need to forget the node before we restart it, to keep the cluster steady
	if redisCluster.Spec.Ephemeral {
		r.LogInfo(redisCluster.NamespacedName(), "Forgetting previous node")
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
	existingStateFulSet, err = r.UpdateStatefulSet(ctx, existingStateFulSet, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not update partition for Statefulset")
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
	r.LogInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(*(existingStateFulSet.Spec.Replicas)))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(*(existingStateFulSet.Spec.Replicas)))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not wait for Pods to become ready")
		return err
	}

	// We want to wait again after the pods become ready,
	// as the Redis Cluster needs to register that the node has joined
	time.Sleep(time.Second * 5)

	r.LogInfo(redisCluster.NamespacedName(), "Fetching new clusterNodes")
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

	r.LogInfo(redisCluster.NamespacedName(), "Meeting Cluster Friends")
	// Due to an existing bug where a restarting node does not update it's own IP,
	// and incorrectly advertises the wrong data,
	// we need to rerun the cluster meet
	err = clusterNodes.ClusterMeet(ctx)
	if err != nil {
		return err
	}

	// We want to wait again to ensure the cluster EPOCH is distributed after the meet
	time.Sleep(time.Second * 5)

	r.LogInfo(redisCluster.NamespacedName(), "Replicating previous master")
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

func (r *RedisClusterReconciler) UpdateStatefulSet(ctx context.Context, statefulSet *v1.StatefulSet, redisCluster *redisv1.RedisCluster) (*v1.StatefulSet, error) {
	refreshedStatefulSet := &v1.StatefulSet{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: statefulSet.Namespace, Name: statefulSet.Name}, refreshedStatefulSet)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedStatefulSet.Labels = statefulSet.Labels
		refreshedStatefulSet.Spec = statefulSet.Spec
		var updateErr = r.Client.Update(ctx, refreshedStatefulSet)
		return updateErr
	})
	if err != nil {
		return nil, err
	}
	return refreshedStatefulSet, nil
}

func (r *RedisClusterReconciler) UpdateDeployment(ctx context.Context, deployment *v1.Deployment, redisCluster *redisv1.RedisCluster) (*v1.Deployment, error) {
	refreshedDeployment := &v1.Deployment{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, refreshedDeployment)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedDeployment.Labels = deployment.Labels
		refreshedDeployment.Spec = deployment.Spec
		var updateErr = r.Client.Update(ctx, refreshedDeployment)
		return updateErr
	})
	if err != nil {
		return nil, err
	}
	return refreshedDeployment, nil
}

func (r *RedisClusterReconciler) UpdatePodDisruptionBudget(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name + "-pdb"}, refreshedPdb)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		if redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal != 0 || redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal != "" {
			refreshedPdb.Spec.MinAvailable = nil
			refreshedPdb.Spec.MaxUnavailable = &redisCluster.Spec.Pdb.PdbSizeUnavailable
		} else {
			refreshedPdb.Spec.MaxUnavailable = nil
			refreshedPdb.Spec.MinAvailable = &redisCluster.Spec.Pdb.PdbSizeAvailable
		}
		refreshedPdb.ObjectMeta.Labels = *redisCluster.Spec.Labels
		refreshedPdb.Spec.Selector.MatchLabels = map[string]string{redis.RedisClusterLabel: redisCluster.ObjectMeta.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"}

		var updateErr = r.Client.Update(ctx, refreshedPdb)
		return updateErr
	})
	if err != nil {
		return err
	}
	return nil
}
func (r *RedisClusterReconciler) DeletePodDisruptionBudget(ctx context.Context, pdb *pv1.PodDisruptionBudget, redisCluster *redisv1.RedisCluster) error {
	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: pdb.Namespace, Name: pdb.Name}, refreshedPdb)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		var updateErr = r.Client.Delete(ctx, refreshedPdb)
		return updateErr
	})
	if err != nil {
		return err
	}
	return nil
}

// GetSidedNodesForScaleDown splits the cluster nodes into two sides. Left and Right.
// This is usually used when a cluster is about to scale down.
// We can work out which nodes will be left after we have scaled down, and which will have bee cut off.
// We can use this to identify nodes that need to be rebalanced,
// and re-orient the cluster so after we remove the additional nodes,
// all of the necessary nodes to fulfill the new cluster, are the ones which have remained
// Left identifies nodes which will remain when we remove additional replicas.
// Right identifies nodes which will be cut off when we update the replica count on the statefulset
func (r *RedisClusterReconciler) GetSidedNodesForScaleDown(ctx context.Context, redisCluster *redisv1.RedisCluster) (masterOnLeft, masterOnRight, replicaOnLeft, replicaOnRight []*kubernetes.ClusterNode, err error) {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return masterOnLeft, masterOnRight, replicaOnLeft, replicaOnRight, err
	}

	expectedReplicas := redisCluster.NodesNeeded()
	podPrefix := fmt.Sprintf("%s-", redisCluster.Name)
	for _, node := range clusterNodes.Nodes {
		ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.Pod.Name, podPrefix))
		if ordinal >= expectedReplicas {
			// Right side
			if node.IsMaster() {
				masterOnRight = append(masterOnRight, node)
			} else {
				replicaOnRight = append(replicaOnRight, node)
			}
		} else {
			if node.IsMaster() {
				masterOnLeft = append(masterOnLeft, node)
			} else {
				replicaOnLeft = append(replicaOnLeft, node)
			}
		}
	}
	return masterOnLeft, masterOnRight, replicaOnLeft, replicaOnRight, err
}

func (r *RedisClusterReconciler) ReOrientClusterForScaleDown(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// Scaling Down.
	// We need to find the pods which are going to be removed so we can rebalance away from them
	// Are we going to have less masters ?
	// If we need 0 replicas, we can reset any replicas to masters to make our lives easier in the next steps
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	podPrefix := fmt.Sprintf("%s-", redisCluster.Name)
	expectedReplicas := int32(redisCluster.NodesNeeded())
	logger := r.GetHelperLogger(redisCluster.NamespacedName())

	// We want to cover cases where there are no replicas needed
	if redisCluster.Spec.ReplicasPerMaster == 0 {
		for _, node := range clusterNodes.Nodes {
			if node.IsReplica() {
				// We need to reset this node,
				err = node.ClusterNode.ClusterResetNode()
				if err != nil {
					return err
				}
				// Wait for the change to propagate before we do the next node
				time.Sleep(5 * time.Second)
			}
		}

		err = clusterNodes.ClusterMeet(ctx)
		if err != nil {
			return err
		}

		// Sleep for the nodes to finish meeting and update
		time.Sleep(5 * time.Second)

		// Reload information to get latest information
		err = clusterNodes.LoadInfoForNodes()
		if err != nil {
			return err
		}

		activeMasters, err := clusterNodes.GetMasters()
		if err != nil {
			return fmt.Errorf("failed getting master list %v", err)
		}

		// Next we need to rebalance to the early ordinals
		// We'll sort the masters by ordinal, and then select the early ordinals to keep
		sort.Slice(activeMasters, func(i, j int) bool {
			ordinalI, _ := strconv.Atoi(strings.TrimPrefix(activeMasters[i].Pod.Name, podPrefix))
			ordinalJ, _ := strconv.Atoi(strings.TrimPrefix(activeMasters[j].Pod.Name, podPrefix))
			return ordinalI < ordinalJ
		})

		// We want to ensure the slots are all stable before we try to rebalance
		err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Cant ensure cluster")
		}

		// Now rebalance to the masters which will remain after we remove some pods.
		weights := map[string]int{}
		deleteMasters := activeMasters[int(redisCluster.Spec.Replicas):]
		for _, deletable := range deleteMasters {
			weights[deletable.ClusterNode.Name()] = 0
		}
		err = clusterNodes.RebalanceCluster(ctx, weights, kubernetes.MoveSlotOption{
			PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
		}, logger)
		if err != nil {
			return err
		}

		return nil
	}

	// We want to cover cases where there are too many masters
	activeMasters, err := clusterNodes.GetMasters()
	if err != nil {
		return err
	}
	if int32(len(activeMasters)) > redisCluster.Spec.Replicas {
		// We are going to have less masters.
		// We need to rebalance away from the masters which are too many, and then reset them
		// We will select the masters in earlier ordinals as the new masters
		sort.Slice(activeMasters, func(i, j int) bool {
			ordinalI, _ := strconv.Atoi(strings.TrimPrefix(activeMasters[i].Pod.Name, podPrefix))
			ordinalJ, _ := strconv.Atoi(strings.TrimPrefix(activeMasters[j].Pod.Name, podPrefix))
			return ordinalI < ordinalJ
		})
		keepMasters := activeMasters[:int(redisCluster.Spec.Replicas)]
		deleteMasters := activeMasters[int(redisCluster.Spec.Replicas):]

		// We want to ensure the slots are all stable before we try to rebalance
		err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
		if err != nil {
			return err
		}

		weights := map[string]int{}
		for _, deletable := range deleteMasters {
			weights[deletable.ClusterNode.Name()] = 0
		}
		err = clusterNodes.RebalanceCluster(ctx, weights, kubernetes.MoveSlotOption{
			PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
		}, logger)
		if err != nil {
			return err
		}
		// We can now reset the deletable masters to replicas of the masters
		// Find masters with too few replicas
		currentKeepable := 0
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
		//TODO wait for replicas to be in sync rather than just time
		time.Sleep(10 * time.Second)
		// At this point the nodes info is outdated and needs to be updated
		err = clusterNodes.LoadInfoForNodes()
		if err != nil {
			return err
		}
	}

	if int32(len(activeMasters)) < redisCluster.Spec.Replicas {
		// We need to call ensureClusterRatio to get the masters back up to what we expect
		// Before we start with the next piece, we need to make sure the ratio of the cluster is correct for safety,
		// and to always have replicas attached.
		err = clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
		if err != nil {
			return err
		}
	}

	err = clusterNodes.LoadInfoForNodes()
	if err != nil {
		return err
	}

	// We need to rebalance to ensure slots spread across the new master set
	err = clusterNodes.RebalanceCluster(ctx, map[string]int{}, kubernetes.MoveSlotOption{
		PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance,
	}, logger)
	if err != nil {
		return err
	}

	// We need to failover all masters on the right, to replicas on the left,
	// to ensure only masters which have no replica on the left remain on the right
	_, masterr, replical, _, err := r.GetSidedNodesForScaleDown(ctx, redisCluster)
	if err != nil {
		return err
	}
	for _, master := range masterr {
		for _, replica := range replical {
			if replica.ClusterNode.Replicate() == master.ClusterNode.Name() {
				// We have found a replica on the left with a master on the right.
				// We need to failover to it now.
				err = replica.ClusterNode.Call("CLUSTER", "FAILOVER").Err()
				if err != nil {
					return err
				}
				// todo check for complete failover rather than specific time
				// give some time for the failover to complete
				time.Sleep(5 * time.Second)
				break
			}
		}
	}

	// We need to identify any masters on the right without replicas on the left
	// We need to update one of the replicas on the left to point at this master,
	// We can then continue to failover left, and continue the re-orientation
	_, masterr, replical, _, err = r.GetSidedNodesForScaleDown(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Check whether there are any masters on the right, with 0 replicas on the left
	var mastersNeedToBeMovedLeft []*kubernetes.ClusterNode
	for _, master := range masterr {
		foundReplica := false
		for _, replica := range replical {
			if replica.ClusterNode.Replicate() == master.ClusterNode.Name() {
				// This master has a replica on the left, and we can skip over
				foundReplica = true
				break
			}
		}
		if !foundReplica {
			// We need to use one of the other replicas on the left
			mastersNeedToBeMovedLeft = append(mastersNeedToBeMovedLeft, master)
		}
	}
	for _, masterNeedsMove := range mastersNeedToBeMovedLeft {
		for _, replica := range replical {
			err = replica.ClusterNode.LoadInfo(false)
			if err != nil {
				return err
			}
			// Make sure the replica is not already replicating one of the masters which need to move
			replicaViable := true
			for _, master := range mastersNeedToBeMovedLeft {
				if master.ClusterNode.Name() == replica.ClusterNode.Replicate() {
					replicaViable = false
				}
			}
			if replicaViable {
				_, err = replica.ClusterNode.ClusterReplicateWithNodeID(masterNeedsMove.ClusterNode.Name())
				if err != nil {
					return err
				}
				break
			}
		}
	}

	// We need to wait for any cluster replicate commands to finish, so we can failover and re-orient
	if len(mastersNeedToBeMovedLeft) > 0 {
		// TODO replcae with wait for replicas in sync
		time.Sleep(10 * time.Second)
	}

	// Now that all of the remaining masters definitely have replicas, we should fail them over.
	// We need to recalculate after making changes
	_, masterr, replical, _, err = r.GetSidedNodesForScaleDown(ctx, redisCluster)
	if err != nil {
		return err
	}
	for _, master := range masterr {
		for _, replica := range replical {
			if replica.ClusterNode.Replicate() == master.ClusterNode.Name() {
				// We have found a replica on the left with a master on the right.
				// We need to failover to it now.
				err = replica.ClusterNode.Call("CLUSTER", "FAILOVER").Err()
				if err != nil {
					return err
				}
				// give some time for the failover to complete
				time.Sleep(5 * time.Second)
				break
			}
		}
	}

	// We have failed over, so we need to recalculate the masters on the left and on the right
	_, masterr, _, _, err = r.GetSidedNodesForScaleDown(ctx, redisCluster)
	if err != nil {
		return err
	}
	err = clusterNodes.LoadInfoForNodes()
	if err != nil {
		return err
	}

	if len(masterr) > 0 {
		return errors.New("there are still masters on the right after trying to move all of them left")
	}

	// There are no masters on the right. We can reorganise, and scale down safely
	// We already have the capability to reshuffle the cluster within clusterNodes,
	// but we only want to reorganise the nodes that will be left.
	// We trust the orientation to now be in a state where we can forget the nodes which are going to be deleted
	var newNodes []*kubernetes.ClusterNode
	for _, node := range clusterNodes.Nodes {
		ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.Pod.Name, podPrefix))
		if int32(ordinal) < expectedReplicas {
			newNodes = append(newNodes, node)
		}
	}
	clusterNodes.Nodes = newNodes
	err = clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
	if err != nil {
		return err
	}

	// Remember to reload clusterNodes if there are additional steps
	clusterNodes, err = r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	return nil
}

func (r *RedisClusterReconciler) ScaleCluster(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	var err error

	sset, sset_err := r.FindExistingStatefulSet(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if sset_err != nil {
		return err
	}

	// When scaling up the cluster and one or more pods are evicted by K8s the cluster can stuck on ScalingUp
	// status. To solve this issue we forget outdated nodes.
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())
	err = clusterNodes.RemoveClusterOutdatedNodes(ctx, redisCluster, r.GetHelperLogger(redisCluster.NamespacedName()))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check outdated nodes")
	}

	// @TODO Cleanup
	readyNodes, err := r.GetReadyNodes(ctx, redisCluster)
	if err != nil {
		return err
	}

	currSsetReplicas := *(sset.Spec.Replicas)

	// By introducing master-replica cluster, the replicas returned by statefulset (which includes replica nodes) and
	// redisCluster's replicas (which is just masters) may not match if replicasPerMaster != 0
	realExpectedReplicas := int32(redisCluster.NodesNeeded())
	r.LogInfo(redisCluster.NamespacedName(), "Expected real replicas", "Master Replicas", redisCluster.Spec.Replicas, "Real Replicas", realExpectedReplicas)

	// scaling down: if data migration takes place, move slots
	if realExpectedReplicas < currSsetReplicas {
		err = r.ReOrientClusterForScaleDown(ctx, redisCluster)
		if err != nil {
			return err
		}

		// and scale the statefulset
		sset.Spec.Replicas = &realExpectedReplicas
		_, err = r.UpdateStatefulSet(ctx, sset, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
			return err
		}

		if redisCluster.Spec.DeletePVC && !redisCluster.Spec.Ephemeral {
			err = r.scaleDownClusterNodes(ctx, redisCluster, realExpectedReplicas)
			if err != nil {
				return err
			}
		}
	}

	// Scaling up and all pods became ready
	r.LogInfo(redisCluster.NamespacedName(), "Review state to scale Up ", "desiredReplicas:", strconv.Itoa(int(redisCluster.Spec.Replicas)), "numReadyNodes: ", strconv.Itoa(len(readyNodes)), "statefulReplicas", strconv.Itoa(int(currSsetReplicas)))

	if int32(redisCluster.NodesNeeded()) == currSsetReplicas && int(realExpectedReplicas) == len(readyNodes) {
		r.LogInfo(redisCluster.NamespacedName(), "Scaling is completed. Running forget unnecessary nodes, clustermeet, rebalance")
		err = r.scaleUpClusterNodes(ctx, redisCluster)
		if err != nil {
			return err
		}
	}

	// If we scaled up, we need to reload the statefulset,
	// as it'sbeen a while since we loaded it, and the status could have changed.
	r.LogInfo(redisCluster.NamespacedName(), "ScaleCluster - updating statefulset replicas", "newsize", realExpectedReplicas)
	sset, err = r.FindExistingStatefulSet(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		return err
	}
	sset.Spec.Replicas = &realExpectedReplicas
	sset, err = r.UpdateStatefulSet(ctx, sset, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		return err
	}

	// Robin deployment update
	if redisCluster.Spec.Robin != nil {
		if redisCluster.Spec.Robin.Template != nil {
			mdep, err := r.FindExistingDeployment(ctx, controllerruntime.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - Cannot find existing robin deployment", "deployment", redisCluster.Name+"-robin")
			} else {
				desiredReplicas := int32(0)
				if *sset.Spec.Replicas > 0 {
					desiredReplicas = int32(1)
				}
				if *mdep.Spec.Replicas != desiredReplicas {
					mdep.Spec.Replicas = &desiredReplicas
					mdep, err = r.UpdateDeployment(ctx, mdep, redisCluster)
					if err != nil {
						r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - Failed to update Deployment replicas")
					} else {
						r.LogInfo(redisCluster.NamespacedName(), "ScaleCluster - Robin Deployment replicas updated", "Replicas", mdep.Spec.Replicas)
					}
				}
			}
		}
	}
	// PodDisruptionBudget update
	// We use currSsetReplicas to check the configured number of replicas (not taking int account the extra pod if created)
	if redisCluster.Spec.Pdb.Enabled && math.Min(float64(redisCluster.Spec.Replicas), float64(currSsetReplicas)) > 1 {
		err = r.UpdatePodDisruptionBudget(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - Failed to update PodDisruptionBudget")
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "ScaleCluster - PodDisruptionBudget updated ", "Name", redisCluster.Name+"-pdb")
		}
	}

	return nil
}

func (r *RedisClusterReconciler) scaleDownClusterNodes(ctx context.Context, redisCluster *redisv1.RedisCluster, realExpectedReplicas int32) error {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	culledNodes := r.getCulledNodes(clusterNodes, redisCluster.Name, realExpectedReplicas)

	for _, node := range culledNodes {
		if err := r.deleteNodePVC(ctx, redisCluster, node); err != nil {
			r.LogError(redisCluster.NamespacedName(), err, fmt.Sprintf("Could not handle PVC for node: %s", node.Pod.Name))
		}
	}
	return nil
}

func (r *RedisClusterReconciler) getCulledNodes(clusterNodes kubernetes.ClusterNodeList, clusterName string, threshold int32) []*kubernetes.ClusterNode {
	podPrefix := fmt.Sprintf("%s-", clusterName)
	var culledNodes []*kubernetes.ClusterNode
	for _, node := range clusterNodes.Nodes {
		ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.Pod.Name, podPrefix))
		if ordinal >= int(threshold) {
			culledNodes = append(culledNodes, node)
		}
	}
	return culledNodes
}

func (r *RedisClusterReconciler) deleteNodePVC(ctx context.Context, redisCluster *redisv1.RedisCluster, node *kubernetes.ClusterNode) error {
	pvc, err := r.GetPersistentVolumeClaim(ctx, r.Client, redisCluster, fmt.Sprintf("data-%s", node.Pod.Name))
	if errors2.IsNotFound(err) {
		r.LogError(redisCluster.NamespacedName(), err, fmt.Sprintf("PVC of node %s doesn't exist. Skipping.", node.Pod.Name))
		return nil
	} else if err != nil {
		return fmt.Errorf("could not get PVC of node %s: %w", node.Pod.Name, err)
	}

	r.LogInfo(redisCluster.NamespacedName(), fmt.Sprintf("Deleting PVC: data-%s", node.Pod.Name))
	return r.DeletePVC(ctx, r.Client, pvc)
}

func (r *RedisClusterReconciler) scaleUpClusterNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		return err
	}
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	if err := r.ensurePodsReadyAndMeet(ctx, redisCluster, clusterNodes); err != nil {
		return err
	}

	if err := clusterNodes.LoadInfoForNodes(); err != nil {
		return err
	}

	if err := r.ensureClusterRatioAndRebalance(ctx, redisCluster, clusterNodes); err != nil {
		return err
	}

	return nil
}

func (r *RedisClusterReconciler) ensurePodsReadyAndMeet(ctx context.Context, redisCluster *redisv1.RedisCluster, clusterNodes kubernetes.ClusterNodeList) error {
	if podsReady, err := r.AllPodsReady(ctx, redisCluster); err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check ready pods")
		return err
	} else if !podsReady {
		return errors.New("pods are not ready yet. Scaling procedure cancelled")
	}

	if err := clusterNodes.ClusterMeet(ctx); err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not join cluster")
		return err
	}

	// Waiting for the meet to propagate through the Redis Cluster.
	time.Sleep(10 * time.Second)

	return nil
}

func (r *RedisClusterReconciler) ensureClusterRatioAndRebalance(ctx context.Context, redisCluster *redisv1.RedisCluster, clusterNodes kubernetes.ClusterNodeList) error {
	if err := clusterNodes.EnsureClusterRatio(ctx, redisCluster, r.GetHelperLogger(redisCluster.NamespacedName())); err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - issue with ensuring cluster ratio when scaling up")
		return err
	}

	options := kubernetes.MoveSlotOption{PurgeKeys: redisCluster.Spec.PurgeKeysOnRebalance}
	if err := clusterNodes.RebalanceCluster(ctx, map[string]int{}, options, r.GetHelperLogger(redisCluster.NamespacedName())); err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - issue with rebalancing cluster when scaling up")
		return err
	}

	return nil
}

func (r *RedisClusterReconciler) isOwnedByUs(o client.Object) bool {
	labels := o.GetLabels()
	if _, found := labels[redis.RedisClusterLabel]; found {
		return true
	}
	return false
}

func (r *RedisClusterReconciler) ClusterMeet(ctx context.Context, nodes map[string]*redisv1.RedisNode, redisCluster *redisv1.RedisCluster) error {
	r.LogInfo(redisCluster.NamespacedName(), "ClusterMeet", "nodes", nodes)
	var rdb *redisclient.Client

	for srcNodeId, srcnode := range nodes {
		for trgNodeId, trgnode := range nodes {
			if trgNodeId == srcNodeId {
				continue
			}
			r.LogInfo(redisCluster.NamespacedName(), "ClusterMeet", "srcnode", srcnode, "trgnode", trgnode)
			rdb, _ = r.GetRedisClientForNode(ctx, srcNodeId, redisCluster)
			_, err := rdb.ClusterMeet(ctx, trgnode.IP, strconv.Itoa(redis.RedisCommPort)).Result()

			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "ClusterMeet failed", "nodes", srcnode)
				return err
			}
		}

	}
	return nil
}

func (r *RedisClusterReconciler) GetSlotsRanges(nodes int32, redisCluster *redisv1.RedisCluster) []*redisv1.SlotRange {
	slots := redis.SplitNodeSlots(int(nodes))
	var apiRedisSlots = make([]*redisv1.SlotRange, 0)
	for _, node := range slots {
		apiRedisSlots = append(apiRedisSlots, &redisv1.SlotRange{Start: node.Start, End: node.End})
	}
	r.LogInfo(redisCluster.NamespacedName(), "GetSlotsRanges", "slots", slots, "ranges", apiRedisSlots)
	return apiRedisSlots
}

func (r *RedisClusterReconciler) NodesBySequence(nodes map[string]*redisv1.RedisNode) ([]string, error) {
	nodesBySequence := make([]string, len(nodes))
	for nodeId, node := range nodes {
		nodeNameElements := strings.Split(node.Name, "-")
		nodePodSequence, err := strconv.Atoi(nodeNameElements[len(nodeNameElements)-1])
		if err != nil {
			return nil, err
		}
		if len(nodes) <= nodePodSequence {
			return nil, fmt.Errorf("race condition with pod sequence: seq:%d, butlen: %d", nodePodSequence, len(nodes))
		}
		nodesBySequence[nodePodSequence] = nodeId
	}
	return nodesBySequence, nil
}

// TODO: check how many cluster slots have been already assign, and rebalance cluster if necessary
func (r *RedisClusterReconciler) AssignSlots(ctx context.Context, nodes map[string]*redisv1.RedisNode, redisCluster *redisv1.RedisCluster) error {
	// when all nodes are formed in a cluster, addslots
	r.LogInfo(redisCluster.NamespacedName(), "AssignSlots", "nodeslen", len(nodes), "nodes", nodes)
	slots := redis.SplitNodeSlots(len(nodes))
	nodesBySequence, _ := r.NodesBySequence(nodes)
	for i, nodeId := range nodesBySequence {
		rdb, err := r.GetRedisClientForNode(ctx, nodeId, redisCluster)
		if err != nil {
			return err
		}

		rdb.ClusterAddSlotsRange(ctx, slots[i].Start, slots[i].End)
		r.LogInfo(redisCluster.NamespacedName(), "Running cluster assign slots", "pods", nodes)
	}
	return nil
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

func (r *RedisClusterReconciler) GetRedisClusterPods(ctx context.Context, redisCluster *redisv1.RedisCluster) *corev1.PodList {
	allPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel:                     redisCluster.Name,
			r.GetStatefulSetSelectorLabel(redisCluster): "redis",
		},
	)

	err := r.Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not list pods")
	}

	return allPods
}

func (r *RedisClusterReconciler) GetReadyNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (map[string]*redisv1.RedisNode, error) {
	return r.GetReadyNodesFunc(ctx, redisCluster)
}

func (r *RedisClusterReconciler) DoGetReadyNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (map[string]*redisv1.RedisNode, error) {
	allPods := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel:                     redisCluster.GetName(),
			r.GetStatefulSetSelectorLabel(redisCluster): "redis",
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
					r.LogInfo(redisCluster.NamespacedName(), "Could not fetch node id", "pod: ", pod.Status.PodIP)
				}
				if nodeId == nil {
					return nil, errors.New("can't fetch node id")
				}
				// Get redis CLI version from info command
				redisCLI := ""
				nodeInfo, err := redisClient.Info(ctx).Result()
				if err != nil {
					r.LogInfo(redisCluster.NamespacedName(), "Could not fetch node info", "pod: ", pod.Status.PodIP)
				} else {
					redisCLI = redis.GetRedisCLIFromInfo(nodeInfo)
				}
				// Get cluster nodes info if not already fetched
				if len(clusterNodes) == 0 {
					nodes, err := redisClient.Do(ctx, "cluster", "nodes").Result()
					if err != nil {
						return nil, errors.New("can't fetch cluster nodes")
					}
					clusterNodes = redis.ParseClusterNodes(nodes.(string))
				}

				readyNodes[nodeId.(string)] = &redisv1.RedisNode{IP: pod.Status.PodIP, Name: pod.GetName(), IsMaster: true, ReplicaOf: "", RedisCLI: redisCLI}

				if value, ok := clusterNodes[nodeId.(string)]; ok {
					readyNodes[nodeId.(string)].IsMaster = value.IsMaster
					readyNodes[nodeId.(string)].ReplicaOf = value.ReplicaOf
				}
			}
		}
	}
	r.LogInfo(redisCluster.NamespacedName(), "GetReadyNodes", "nodes", readyNodes, "numReadyNodes", strconv.Itoa(len(readyNodes)))
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

func makeRangeMap(min int, max int) map[int]struct{} {
	result := map[int]struct{}{}
	a := make([]int, max-min+1)
	for i := range a {
		result[min+i] = struct{}{}
	}
	return result
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

	updatedAnnotations[CONFIG_CHECKSUM_ANNOTATION] = checksum
	statefulSet.Spec.Template.Annotations = updatedAnnotations

	return statefulSet
}

// Checks if the configuration has changed.
// If the annotation StatefulSet.Spec.Template.Annotations[CONFIG_CHECKSUM_ANNOTATION]  exists, we check first if the
// configuration checksum has changed.
// If not, we compare the configuration properties.
func (r *RedisClusterReconciler) isConfigChanged(redisCluster *redisv1.RedisCluster, statefulSet *v1.StatefulSet,
	configMap *corev1.ConfigMap) (bool, string) {
	// Comparing config checksums
	if statefulSet.Spec.Template.Annotations != nil {
		checksum, exists := statefulSet.Spec.Template.Annotations[CONFIG_CHECKSUM_ANNOTATION]
		if exists {
			calculatedChecksum := calculateRCConfigChecksum(redisCluster)
			if checksum != calculatedChecksum {
				r.LogInfo(redisCluster.NamespacedName(), "Config checksum changed", "existing checksum", checksum, "calculated checksum", calculatedChecksum)
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

func (r *RedisClusterReconciler) updateSubStatus(ctx context.Context, redisCluster *redisv1.RedisCluster, substatus string, partition int) error {
	refreshedRedisCluster := redisv1.RedisCluster{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
		return err
	}
	refreshedRedisCluster.Status.Substatus.Status = substatus
	refreshedRedisCluster.Status.Substatus.UpgradingPartition = partition

	err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}
	return nil
}
