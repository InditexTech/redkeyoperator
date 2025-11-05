// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"reflect"
	"strconv"
	"strings"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	redis "github.com/inditextech/redkeyoperator/internal/redis"
	"github.com/inditextech/redkeyoperator/internal/robin"

	errors2 "k8s.io/apimachinery/pkg/api/errors"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ConfigChecksumAnnotation = "inditex.dev/redis-conf"
)

// RedKey cluster is set to 0 replicas
//
//	 -> terminate all cluster pods (StatefulSet replicas set to 0)
//	 -> terminate robin pod (Deployment replicas set to 0)
//	 -> delete pdb
//	 -> RedKey cluster status set to 'Ready'
//		-> All conditions set to false
func (r *RedKeyClusterReconciler) clusterScaledToZeroReplicas(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) error {
	r.logInfo(redkeyCluster.NamespacedName(), "Cluster spec replicas is set to 0", "SpecReplicas", redkeyCluster.Spec.Replicas)
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Cannot find exists statefulset maybe is deleted.")
	}
	if sset != nil {
		if *(sset.Spec.Replicas) != 0 {
			r.logInfo(redkeyCluster.NamespacedName(), "Cluster scaled to 0 replicas")
			r.Recorder.Event(redkeyCluster, "Normal", "RedKeyClusterScaledToZero", fmt.Sprintf("Scaling down from %d to 0", *(sset.Spec.Replicas)))
		}
		*sset.Spec.Replicas = 0
		sset, err = r.updateStatefulSet(ctx, sset, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet")
		}
		r.logInfo(redkeyCluster.NamespacedName(), "StatefulSet updated", "Replicas", sset.Spec.Replicas)
	}

	r.scaleDownRobin(ctx, redkeyCluster)

	r.deletePodDisruptionBudget(ctx, redkeyCluster)

	// All conditions set to false. Status set to Ready.
	var update_err error
	if !reflect.DeepEqual(redkeyCluster.Status, redkeyv1.StatusReady) {
		redkeyCluster.Status.Status = redkeyv1.StatusReady
		setAllConditionsFalse(r.getHelperLogger(redkeyCluster.NamespacedName()), redkeyCluster)
		update_err = r.updateClusterStatus(ctx, redkeyCluster)
	}

	return update_err
}

func (r *RedKeyClusterReconciler) upgradeCluster(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) error {

	// If a Fast Upgrade is possible or already en progress, start it or check if it's already finished.
	// In both situations we return here.
	fastUpgrade, err := r.doFastUpgrade(ctx, redkeyCluster)
	if err != nil || fastUpgrade {
		return err
	}

	// Continue doing a Slow Upgrade if we can't go the Fast way.
	return r.doSlowUpgrade(ctx, redkeyCluster)
}

// If PurgeKeysOnRebalance flag is active and RedKey cluster is not configures as master-replica we can
// do a Fast Upgrade, applying the changes to the StatefulSet and recreaing it. Slots move will be avoided.
func (r *RedKeyClusterReconciler) doFastUpgrade(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusFastUpgrading:
		// Already Fast upgrading. Check if the node pods are ready to start rebuilding the cluster.
		r.logInfo(redkeyCluster.NamespacedName(), "Retaking Fast Upgrade")

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      redkeyCluster.Name,
				Namespace: redkeyCluster.Namespace,
			},
		}
		existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
		if err != nil {
			return false, err
		}
		logger := r.getHelperLogger(redkeyCluster.NamespacedName())

		podsReady, err := r.allPodsReady(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Fast Upgrade", "expectedReplicas", int(*(existingStatefulSet.Spec.Replicas)))
			return true, nil
		}

		// Rebuild the cluster
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		err = robin.ClusterFix()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error performing a cluster fix through Robin")
			return true, err
		}

		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingFastUpgrading, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil

	case redkeyv1.SubstatusEndingFastUpgrading:
		// Rebuilding the cluster after recreating all node pods. Check if the cluster is ready to end the Fast upgrade.
		logger := r.getHelperLogger(redkeyCluster.NamespacedName())
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		check, errors, warnings, err := robin.ClusterCheck()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness before ending the fast upgrade", "errors", errors, "warnings", warnings)
			return true, nil
		}

		// The cluster is now upgraded, and we can set the Cluster Status as Ready again and remove the Substatus.
		redkeyCluster.Status.Status = redkeyv1.StatusReady
		redkeyCluster.Status.Substatus.Status = ""
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionUpgrading)

		return true, nil
	default:
		// Fast upgrade start: If purgeKeysOnRebalance property is set to 'true' and we have no replicas. No need to iterate over the partitions.
		// If fast upgrade is not allowed but we have no replicas, the cluster must be scaled up adding one extra
		// node to be able to move slots and keys in order to ensure keys are preserved.
		if redkeyCluster.Spec.PurgeKeysOnRebalance && redkeyCluster.Spec.ReplicasPerMaster == 0 {
			r.logInfo(redkeyCluster.NamespacedName(), "Fast upgrade will be performed")

			err := r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusFastUpgrading, "")
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}

			// Update configuration: changes in configuration, labels and overrides are persisted before upgrading
			existingStatefulSet, err := r.upgradeClusterConfigurationUpdate(ctx, redkeyCluster)
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
func (r *RedKeyClusterReconciler) doSlowUpgrade(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) error {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redkeyCluster.Name,
			Namespace: redkeyCluster.Namespace,
		},
	}

	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}

	// We need to ensure that the upgrade is really necessary,
	// and we are not just host to a double reconciliation attempt
	if redkeyCluster.Status.Substatus.Status == "" {
		err = r.updateUpgradingStatus(ctx, redkeyCluster)
		if err != nil {
			return fmt.Errorf("could not update upgrading status: %w", err)
		}
	}
	if redkeyCluster.Status.Status != redkeyv1.StatusUpgrading {
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

	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusUpgradingScalingUp:
		err = r.doSlowUpgradeScalingUp(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusSlowUpgrading:
		err = r.doSlowUpgradeUpgrading(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusEndingSlowUpgrading:
		err = r.doSlowUpgradeEnd(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusUpgradingScalingDown:
		err = r.doSlowUpgradeScalingDown(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusRollingConfig:
		err = r.doSlowUpgradeRollingUpdate(ctx, redkeyCluster, existingStatefulSet)
	default:
		err = r.doSlowUpgradeStart(ctx, redkeyCluster, existingStatefulSet)
	}

	return err
}

func (r *RedKeyClusterReconciler) doSlowUpgradeScalingUp(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Check Redis node pods rediness
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Set the number of replicas to Robin to have the new node met to the existing nodes.
	replicas, replicasPerMaster, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting replicas from Robin")
		return err
	}
	if replicas != int(redkeyCluster.Spec.Replicas) || replicasPerMaster != int(redkeyCluster.Spec.ReplicasPerMaster) {
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas")
		}
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin replicas")
			return err
		}
	}

	// Check all cluster nodes are ready from Robin.
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}
	if len(clusterNodes.Nodes) != int(*existingStatefulSet.Spec.Replicas) {
		r.logInfo(redkeyCluster.NamespacedName(), "Not all cluster nodes are yet ready from Robin")
		return nil // Not all nodes ready --> Keep waiting
	}

	// Check cluster health.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Update substatus.
	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusSlowUpgrading, "")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedKeyClusterReconciler) doSlowUpgradeUpgrading(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Check Redis node pods rediness.
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	// Get Robin.
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Check all cluster nodes are ready from Robin.
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}
	if len(clusterNodes.Nodes) != int(*existingStatefulSet.Spec.Replicas) {
		r.logInfo(redkeyCluster.NamespacedName(), "Not all cluster nodes are yet ready from Robin")
		return nil // Not all nodes ready --> Keep waiting
	}

	// Check cluster health.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Get the current partition and update Upgrading Partition in RedKeyCluster Status if starting iterating over partitions.
	var currentPartition int
	if redkeyCluster.Status.Substatus.UpgradingPartition == "" {
		currentPartition = int(*(existingStatefulSet.Spec.Replicas)) - 1
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusSlowUpgrading, strconv.Itoa(currentPartition))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else {
		currentPartition, err = strconv.Atoi(redkeyCluster.Status.Substatus.UpgradingPartition)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Upgrading Partition from RedKeyCluster object")
			return err
		}
	}

	// If first iteration over partitions: update configuration.
	// Else: Move slots away from partition and rolling update (don't do over the extra node to optimize).
	if currentPartition == int(*(existingStatefulSet.Spec.Replicas))-1 {
		// Update configuration: changes in configuration, labels and overrides are persisted before upgrading
		existingStatefulSet, err = r.upgradeClusterConfigurationUpdate(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Cluster configuration")
			return err
		}
	} else {
		// Move slots from partition before rolling update.
		completed, err := redkeyRobin.MoveSlots(currentPartition, currentPartition+1, 0)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error moving slots", "From node", currentPartition, "To node", currentPartition+1)
			return err
		}
		if !completed {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting to complete moving slots", "From node", currentPartition, "To node", currentPartition+1)
			return nil // Move slots not completed --> keep waiting
		}
	}

	// Stop Robin reconciliation
	err = redkeyRobin.SetAndPersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.RobinStatusNoReconciling)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating/persisting Robin status", "status", redkeyv1.RobinStatusNoReconciling)
		return err
	}

	// RollingUpdate
	r.logInfo(redkeyCluster.NamespacedName(), "Executing partition Rolling Update", "partition", currentPartition)
	localPartition := int32(currentPartition)
	existingStatefulSet.Spec.UpdateStrategy = v1.StatefulSetUpdateStrategy{
		Type:          v1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &v1.RollingUpdateStatefulSetStrategy{Partition: &localPartition},
	}
	_, err = r.updateStatefulSet(ctx, existingStatefulSet, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not update partition for Statefulset")
		return err
	}

	// Reset node
	err = redkeyRobin.ClusterResetNode(currentPartition)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error from Robin forgeting the node", "node index", currentPartition)
		return err
	}

	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusRollingConfig, strconv.Itoa(currentPartition))
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedKeyClusterReconciler) doSlowUpgradeRollingUpdate(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	var err error

	// Check Redis node pods rediness.
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	// Get Robin.
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Check all cluster nodes are ready from Robin.
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}
	if len(clusterNodes.Nodes) != int(*existingStatefulSet.Spec.Replicas) {
		r.logInfo(redkeyCluster.NamespacedName(), "Not all cluster nodes are yet ready from Robin")
		return nil // Not all nodes ready --> Keep waiting
	}

	// Check cluster health.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Get the current partition and update Upgrading Partition in RedKeyCluster Status if starting iterating over partitions.
	var currentPartition int
	if redkeyCluster.Status.Substatus.UpgradingPartition == "" {
		currentPartition = int(*(existingStatefulSet.Spec.Replicas)) - 1
		err := r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusSlowUpgrading, strconv.Itoa(currentPartition))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else {
		currentPartition, err = strconv.Atoi(redkeyCluster.Status.Substatus.UpgradingPartition)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Upgrading Partition from RedKeyCluster object")
			return err
		}
	}

	// If first partition reached, we can move to the next step.
	// Else step over to the next partition.
	if currentPartition == 0 {
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingSlowUpgrading, strconv.Itoa(currentPartition))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else {
		nextPartition := currentPartition - 1
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusSlowUpgrading, strconv.Itoa(nextPartition))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	}

	// Restart Robin reconciliation
	err = redkeyRobin.SetAndPersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.RobinStatusUpgrading)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating/persisting Robin status", "status", redkeyv1.RobinStatusUpgrading)
		return err
	}

	return nil
}

func (r *RedKeyClusterReconciler) doSlowUpgradeEnd(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Check Redis node pods rediness (pod from last rolling update could be not ready yet).
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	// Get Robin.
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Check all cluster nodes are ready from Robin.
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}
	if len(clusterNodes.Nodes) != int(*existingStatefulSet.Spec.Replicas) {
		r.logInfo(redkeyCluster.NamespacedName(), "Not all cluster nodes are yet ready from Robin")
		return nil // Not all nodes ready --> Keep waiting
	}

	// Check cluster health.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Move slots from extra node to node 0.
	extraNodeIndex := int(*(existingStatefulSet.Spec.Replicas)) - 1
	completed, err := redkeyRobin.MoveSlots(extraNodeIndex, 0, 0)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error moving slots", "From node", extraNodeIndex, "To node", 0)
		return err
	}
	if !completed {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting to complete moving slots", "From node", extraNodeIndex, "To node", 0)
		return nil // Move slots not completed --> keep waiting
	}

	// ScaleDown the cluster
	r.logInfo(redkeyCluster.NamespacedName(), "Scaling down the cluster to remove the extra node")
	_, err = r.updateRdclReplicas(ctx, redkeyCluster, redkeyCluster.Spec.Replicas-1)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update RedKeyCluster replicas")
	}
	*existingStatefulSet.Spec.Replicas = *existingStatefulSet.Spec.Replicas - 1
	_, err = r.updateStatefulSet(ctx, existingStatefulSet, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
		return err
	}

	// Reset node
	err = redkeyRobin.ClusterResetNode(extraNodeIndex)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error from Robin forgeting the node", "node index", extraNodeIndex)
		return err
	}

	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusUpgradingScalingDown, "0")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedKeyClusterReconciler) doSlowUpgradeScalingDown(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Check Redis node pods rediness.
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin")
		return err
	}

	// Set the number of replicas to Robin to have the new node met to the existing nodes.
	replicas, replicasPerMaster, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting replicas from Robin")
		return err
	}
	if replicas != int(redkeyCluster.Spec.Replicas) || replicasPerMaster != int(redkeyCluster.Spec.ReplicasPerMaster) {
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas")
		}
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin replicas")
			return err
		}
	}

	// Reset node
	err = redkeyRobin.ClusterResetNode(int(redkeyCluster.Spec.Replicas) + 1)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error from Robin forgeting the node", "node index", int(redkeyCluster.Spec.Replicas)+1)
		return err
	}

	// Check all cluster nodes are ready from Robin.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness before ending the fast upgrade", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Update substatus.
	redkeyCluster.Status.Status = redkeyv1.StatusReady
	redkeyCluster.Status.Substatus.Status = ""
	redkeyCluster.Status.Substatus.UpgradingPartition = ""
	setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionUpgrading)

	return nil
}

func (r *RedKeyClusterReconciler) doSlowUpgradeStart(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// ScaleUp the cluster adding one extra node before start upgrading.
	r.logInfo(redkeyCluster.NamespacedName(), "Scaling up the cluster to add one extra node before upgrading")
	_, err := r.updateRdclReplicas(ctx, redkeyCluster, redkeyCluster.Spec.Replicas+1)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update RedKeyCluster replicas")
	}
	*existingStatefulSet.Spec.Replicas = *existingStatefulSet.Spec.Replicas + 1
	_, err = r.updateStatefulSet(ctx, existingStatefulSet, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
		return err
	}
	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusUpgradingScalingUp, "")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

// Updates and persists the StatefulSet configuration and labels, including overrides.
func (r *RedKeyClusterReconciler) upgradeClusterConfigurationUpdate(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (*v1.StatefulSet, error) {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redkeyCluster.Name,
			Namespace: redkeyCluster.Namespace,
		},
	}

	// RedKeyCluster .Spec.Labels
	mergedLabels := *redkeyCluster.Spec.Labels
	defaultLabels := map[string]string{
		redis.RedKeyClusterLabel:                     redkeyCluster.Name,
		r.getStatefulSetSelectorLabel(redkeyCluster): "redis",
	}
	maps.Copy(mergedLabels, defaultLabels)

	// Get configmap
	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		return nil, err
	}
	configMap.Labels = mergedLabels

	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(redkeyCluster)
	r.logInfo(redkeyCluster.NamespacedName(), "Updating configmap", "configmap", configMap.Name)
	// Update ConfigMap
	err = r.Client.Update(ctx, configMap)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when creating configmap")
		return nil, err
	}

	// Get Service
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		return nil, err
	}
	// Update the Service labels
	maps.Copy(service.Labels, mergedLabels)
	r.logInfo(redkeyCluster.NamespacedName(), "Updating service", "service", service.Name)
	err = r.Client.Update(ctx, service)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when updating service")
	}

	// Get existing StateFulSet
	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return nil, err
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Updating statefulset", "StateFulSet", existingStatefulSet.Name)
	// Update StateFulSet .Spec.Template.Labels
	existingStatefulSet.Labels = mergedLabels

	// Add labels from override
	if redkeyCluster.Spec.Override.StatefulSet != nil && redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Labels != nil {
		maps.Copy(mergedLabels, redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Labels)
	}

	existingStatefulSet.Spec.Template.ObjectMeta.Labels = mergedLabels
	// Update StateFulSet .Spec.Template.Spec.Containers[0].Resources
	if redkeyCluster.Spec.Resources != nil && existingStatefulSet.Spec.Template.Spec.Containers != nil && len(existingStatefulSet.Spec.Template.Spec.Containers) > 0 {
		existingStatefulSet.Spec.Template.Spec.Containers[0].Resources = *redkeyCluster.Spec.Resources
		existingStatefulSet.Spec.Template.Spec.Containers[0].Image = redkeyCluster.Spec.Image
	}
	// Update StatefulSet annotations with calculated config checksum if needed
	existingStatefulSet = r.addConfigChecksumAnnotation(existingStatefulSet, redkeyCluster)

	// Handle changes in spec.override
	existingStatefulSet, _ = r.overrideStatefulSet(req, redkeyCluster, existingStatefulSet)

	return existingStatefulSet, nil
}

func (r *RedKeyClusterReconciler) scaleCluster(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {

	// If a Fast Scaling is possible or already en progress, start it or check if it's already finished.
	// In both situations we return here.
	fastScaling, err := r.doFastScaling(ctx, redkeyCluster)
	if err != nil || fastScaling {
		return true, err
	}

	// Continue doing a Slow Scaling if we can't go the Fast way.
	return r.doSlowScaling(ctx, redkeyCluster)
}

func (r *RedKeyClusterReconciler) doFastScaling(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redkeyCluster.Name,
			Namespace: redkeyCluster.Namespace,
		},
	}
	existingStatefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return false, err
	}
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())

	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusFastScaling:
		// Already Fast scaling.
		r.logInfo(redkeyCluster.NamespacedName(), "Retaking Fast Scaling")

		podsReady, err := r.allPodsReady(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Fast Scaling", "expectedReplicas", int(*(existingStatefulSet.Spec.Replicas)))
			return true, nil
		}

		// Reset the cluster
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		err = robin.ClusterRecreate()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error performing a cluster recreate through Robin")
			return true, err
		}

		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingFastScaling, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil
	case redkeyv1.SubstatusEndingFastScaling:
		// Rebuilding the cluster after recreating all node pods. Check if the cluster is ready to end the Fast scaling.
		logger := r.getHelperLogger(redkeyCluster.NamespacedName())
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		check, errors, warnings, err := robin.ClusterCheck()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness before ending the fast scaling", "errors", errors, "warnings", warnings)
			return true, nil
		}

		// The cluster is now scaled, and we can set the Cluster Status as Ready again and remove the Substatus.
		redkeyCluster.Status.Status = redkeyv1.StatusReady
		redkeyCluster.Status.Substatus.Status = ""

		return true, nil
	default:
		// Fast scaling start: If purgeKeysOnRebalance property is set to 'true' and we have no replicas. No need to iterate over the partitions.
		if redkeyCluster.Spec.PurgeKeysOnRebalance && redkeyCluster.Spec.ReplicasPerMaster == 0 {
			r.logInfo(redkeyCluster.NamespacedName(), "Fast upgrading will be performed")

			err := r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusFastScaling, "")
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}

			// Set Robin status to NotReconciling.
			redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
				return true, err
			}
			err = redkeyRobin.SetAndPersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.RobinStatusNoReconciling)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating/persisting Robin status", "status", redkeyv1.RobinStatusNoReconciling)
				return true, err
			}

			// ** FAST SCALING start **
			r.Client.Delete(ctx, existingStatefulSet)

			// Update Robin replicas.
			err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error setting Robin replicas", "replicas", redkeyCluster.Spec.Replicas,
					"replicas per master", redkeyCluster.Spec.ReplicasPerMaster)
				return true, err
			}
			err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas", "replicas", redkeyCluster.Spec.Replicas,
					"replicas per master", redkeyCluster.Spec.ReplicasPerMaster)
			}

			return true, nil
		}
	}

	return false, nil
}

func (r *RedKeyClusterReconciler) doSlowScaling(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	var err error

	// By introducing master-replica cluster, the replicas returned by statefulset (which includes replica nodes) and
	// redkeyCluster's replicas (which is just masters) may not match if replicasPerMaster != 0
	realExpectedReplicas := int32(redkeyCluster.NodesNeeded())
	r.logInfo(redkeyCluster.NamespacedName(), "Expected cluster nodes", "Master nodes", redkeyCluster.Spec.Replicas, "Total nodes (including replicas)", realExpectedReplicas)

	sset, sset_err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if sset_err != nil {
		return true, err
	}
	currSsetReplicas := *(sset.Spec.Replicas)

	if realExpectedReplicas == currSsetReplicas {
		// Complete scaling
		if redkeyCluster.Status.Status == redkeyv1.StatusScalingUp {
			immediateRequeue, err := r.completeClusterScaleUp(ctx, redkeyCluster)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		} else {
			immediateRequeue, err := r.completeClusterScaleDown(ctx, redkeyCluster)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		}
	} else if realExpectedReplicas < currSsetReplicas {
		// Scaling down
		if redkeyCluster.Status.Substatus.Status == "" {
			err := r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusScalingRobin, "")
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}
		}
		immediateRequeue, err := r.scaleDownCluster(ctx, redkeyCluster)
		if err != nil || immediateRequeue {
			return true, err
		}
	} else {
		// Scaling up
		if redkeyCluster.Status.Substatus.Status == "" {
			err := r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusScalingPods, "")
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
				return true, err
			}
		}
		err = r.scaleUpCluster(ctx, redkeyCluster, realExpectedReplicas)
		if err != nil {
			return true, err
		}
	}

	// PodDisruptionBudget update
	// We use currSsetReplicas to check the configured number of replicas (not taking int account the extra pod if created)
	if redkeyCluster.Spec.Pdb.Enabled && math.Min(float64(redkeyCluster.Spec.Replicas), float64(currSsetReplicas)) > 1 {
		err = r.updatePodDisruptionBudget(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "ScaleCluster - Failed to update PodDisruptionBudget")
		} else {
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - PodDisruptionBudget updated ", "Name", redkeyCluster.Name+"-pdb")
		}
	}

	return false, nil
}

func (r *RedKeyClusterReconciler) scaleDownCluster(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {

	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	// Update replicas
	replicas, replicasPerMaster, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin replicas")
		return true, err
	}
	if replicas != int(redkeyCluster.Spec.Replicas) || replicasPerMaster != int(redkeyCluster.Spec.ReplicasPerMaster) {
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin replicas", "replicas", redkeyCluster.Spec.Replicas,
				"replicas per master", redkeyCluster.Spec.ReplicasPerMaster)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Robin replicas updated", "replicas before", replicas, "replicas per master before", replicasPerMaster,
			"replicas after", redkeyCluster.Spec.Replicas, "replicas per master after", redkeyCluster.Spec.ReplicasPerMaster)
	}

	// Check cluster replicas meet the requirements
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin nodes info")
		return true, err
	}
	existingMasterNodes := len(clusterNodes.GetMasterNodes())
	existingReplicaNodes := len(clusterNodes.GetReplicaNodes())
	expectedMasterNodes := int(redkeyCluster.Spec.Replicas)
	expectedRelicaNodes := int(redkeyCluster.Spec.Replicas * redkeyCluster.Spec.ReplicasPerMaster)
	if existingMasterNodes != expectedMasterNodes || existingReplicaNodes != expectedRelicaNodes {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin to scale the cluster", "expected master nodes", expectedMasterNodes,
			"existing master nodes", existingMasterNodes, "expected replica nodes", expectedRelicaNodes,
			"existing replica nodes", existingReplicaNodes)
		return true, nil // Requeue
	}

	// Update StatefulSet
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		return true, err
	}
	sset.Spec.Replicas = &redkeyCluster.Spec.Replicas
	_, err = r.updateStatefulSet(ctx, sset, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet")
		return true, err
	}
	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusScalingPods, "")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return true, err
	}

	return false, nil
}

func (r *RedKeyClusterReconciler) completeClusterScaleDown(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusScalingPods:
		// StatefulSet has been updated with new replicas/replicasPerMaster at this point.
		// We will ensure all pods are Ready and, then, update the new replicas in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Scaling down", "expected nodes", redkeyCluster.NodesNeeded())
			return true, nil
		}

		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingScaling, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		if redkeyCluster.Spec.DeletePVC && !redkeyCluster.Spec.Ephemeral {
			culledNodes, err := r.getCulledNodes(ctx, redkeyCluster)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error getting culled nodes")
				return true, err
			}

			for _, node := range culledNodes {
				if err := r.deleteNodePVC(ctx, redkeyCluster, &node); err != nil {
					r.logError(redkeyCluster.NamespacedName(), err, fmt.Sprintf("Could not handle PVC for node: %s", node.Name))
					return true, err
				}
			}
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redkeyv1.SubstatusEndingScaling:
		// Final step: ensure the cluster is Ok.

		logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
			return true, err
		}
		check, errors, warnings, err := robin.ClusterCheck()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			// Cluster scaling not completed -> requeue
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - Waiting for cluster readiness before ending the cluster scaling", "errors", errors, "warnings", warnings)
			return true, nil
		}

	default:
		r.logError(redkeyCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redkeyCluster.Status.Substatus.Status)
		return true, nil
	}

	// Cluster scaling completed!
	return false, nil
}

func (r *RedKeyClusterReconciler) getCulledNodes(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) ([]robin.Node, error) {
	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return nil, err
	}
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes info from Robin")
		return nil, err
	}

	podPrefix := fmt.Sprintf("%s-", redkeyCluster.Name)
	var culledNodes []robin.Node
	for _, node := range clusterNodes.Nodes {
		ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.Name, podPrefix))
		if ordinal >= int(redkeyCluster.Spec.Replicas) {
			culledNodes = append(culledNodes, node)
		}
	}

	return culledNodes, nil
}

func (r *RedKeyClusterReconciler) deleteNodePVC(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, node *robin.Node) error {
	pvc, err := r.getPersistentVolumeClaim(ctx, r.Client, redkeyCluster, fmt.Sprintf("data-%s", node.Name))
	if errors2.IsNotFound(err) {
		r.logError(redkeyCluster.NamespacedName(), err, fmt.Sprintf("PVC of node %s doesn't exist. Skipping.", node.Name))
		return nil
	} else if err != nil {
		return fmt.Errorf("could not get PVC of node %s: %w", node.Name, err)
	}

	r.logInfo(redkeyCluster.NamespacedName(), fmt.Sprintf("Deleting PVC: data-%s", node.Name))
	return r.deletePVC(ctx, r.Client, pvc)
}

func (r *RedKeyClusterReconciler) scaleUpCluster(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, realExpectedReplicas int32) error {
	r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - updating statefulset replicas", "newsize", realExpectedReplicas)

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		return err
	}

	sset.Spec.Replicas = &realExpectedReplicas
	_, err = r.updateStatefulSet(ctx, sset, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet")
		return err
	}

	return nil
}

func (r *RedKeyClusterReconciler) completeClusterScaleUp(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusScalingPods:
		// StatefulSet has been updated with new replicas/replicasPerMaster at this point.
		// We will ensure all pods are Ready and, then, update the new replicas in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Scaling up", "expected nodes", redkeyCluster.NodesNeeded())
			return true, nil
		}

		// Update Robin with new replicas/replicasPerMaster
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating replicas in Robin", "replicas", redkeyCluster.Spec.Replicas, "replicasPerMaster", redkeyCluster.Spec.ReplicasPerMaster)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, err
		}

		// Update the status to continue completing the cluster scaling
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusScalingRobin, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redkeyv1.SubstatusScalingRobin:
		// Robin was already updated with new replicas/replicasPerMaster.
		// We will ensure that all cluster nodes are initialized.

		clusterNodes, err := redkeyRobin.GetClusterNodes()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
			return true, err
		}
		masterNodes := clusterNodes.GetMasterNodes()
		if len(masterNodes) != int(redkeyCluster.Spec.Replicas) {
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - Inconsistency. Statefulset replicas equals to RedKeyCluster replicas but we have a different number of cluster nodes. Waiting for Robin to complete scaling up...",
				"RedKeyCluster replicas", redkeyCluster.Spec.Replicas, "Cluster nodes", masterNodes)
		}
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingScaling, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redkeyv1.SubstatusEndingScaling:
		// Final step: ensure the cluster is Ok.

		check, errors, warnings, err := redkeyRobin.ClusterCheck()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
			return true, err
		}
		if !check {
			// Cluster scaling not completed -> requeue
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - Waiting for cluster readiness before ending the cluster scaling", "errors", errors, "warnings", warnings)
			return true, nil
		}

	default:
		r.logError(redkeyCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redkeyCluster.Status.Substatus.Status)
		return true, nil
	}

	// Cluster scaling completed!
	return false, nil
}

func (r *RedKeyClusterReconciler) isOwnedByUs(o client.Object) bool {
	labels := o.GetLabels()
	if _, found := labels[redis.RedKeyClusterLabel]; found {
		return true
	}
	return false
}

func (r *RedKeyClusterReconciler) GetRedisSecret(redkeyCluster *redkeyv1.RedKeyCluster) (string, error) {
	if redkeyCluster.Spec.Auth.SecretName == "" {
		return "", nil
	}

	secret := &corev1.Secret{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: redkeyCluster.Spec.Auth.SecretName, Namespace: redkeyCluster.Namespace}, secret)
	if err != nil {
		return "", err
	}
	redisSecret := string(secret.Data["requirepass"])
	return redisSecret, nil
}

func calculateRCConfigChecksum(redkeyCluster *redkeyv1.RedKeyCluster) string {
	hash := md5.Sum([]byte(redkeyCluster.Spec.Config))
	return hex.EncodeToString(hash[:])
}

// Updates StatefulSet annotations with the config checksum (annotation "config-checksum")
func (r *RedKeyClusterReconciler) addConfigChecksumAnnotation(statefulSet *v1.StatefulSet, redkeyCluster *redkeyv1.RedKeyCluster) *v1.StatefulSet {
	var updatedAnnotations map[string]string

	checksum := calculateRCConfigChecksum(redkeyCluster)

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
func (r *RedKeyClusterReconciler) isConfigChanged(redkeyCluster *redkeyv1.RedKeyCluster, statefulSet *v1.StatefulSet,
	configMap *corev1.ConfigMap) (bool, string) {
	// Comparing config checksums
	if statefulSet.Spec.Template.Annotations != nil {
		checksum, exists := statefulSet.Spec.Template.Annotations[ConfigChecksumAnnotation]
		if exists {
			calculatedChecksum := calculateRCConfigChecksum(redkeyCluster)
			if checksum != calculatedChecksum {
				r.logInfo(redkeyCluster.NamespacedName(), "Config checksum changed", "existing checksum", checksum, "calculated checksum", calculatedChecksum)
				return true, "Redis Config Changed - Checksum"
			}
			return false, ""
		}
	}
	// Comparing properties
	desiredConfig := redis.MergeWithDefaultConfig(
		redis.ConfigStringToMap(redkeyCluster.Spec.Config),
		redkeyCluster.Spec.Ephemeral,
		redkeyCluster.Spec.ReplicasPerMaster)
	observedConfig := redis.ConfigStringToMap(configMap.Data["redis.conf"])
	if !reflect.DeepEqual(observedConfig, desiredConfig) {
		return true, "Redis Config Changed - properties"
	}
	return false, ""
}
