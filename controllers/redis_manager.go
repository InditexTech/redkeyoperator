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

// Redkey cluster is set to 0 primaries
//
//	 -> terminate all cluster pods (StatefulSet replicas set to 0)
//	 -> terminate robin pod (Deployment primaries set to 0)
//	 -> delete pdb
//	 -> Redkey cluster status set to 'Ready'
//		-> All conditions set to false
func (r *RedkeyClusterReconciler) clusterScaledToZeroPrimaries(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	r.logInfo(redkeyCluster.NamespacedName(), "Cluster spec primaries is set to 0", "SpecPrimaries", redkeyCluster.Spec.Primaries)
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Cannot find exists statefulset maybe is deleted.")
	}
	if sset != nil {
		if *(sset.Spec.Replicas) != 0 {
			r.logInfo(redkeyCluster.NamespacedName(), "Cluster scaled to 0 primaries")
			r.Recorder.Event(redkeyCluster, "Normal", "RedkeyClusterScaledToZero", fmt.Sprintf("Scaling down from %d to 0", *(sset.Spec.Replicas)))
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

func (r *RedkeyClusterReconciler) upgradeCluster(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {

	// If a Fast Upgrade is possible or already en progress, start it or check if it's already finished.
	// In both situations we return here.
	fastUpgrade, err := r.doFastUpgrade(ctx, redkeyCluster)
	if err != nil || fastUpgrade {
		return err
	}

	// Continue doing a Slow Upgrade if we can't go the Fast way.
	return r.doSlowUpgrade(ctx, redkeyCluster)
}

// If PurgeKeysOnRebalance flag is active and Redkey cluster is not configures as primary-replica we can
// do a Fast Upgrade, applying the changes to the StatefulSet and recreaing it. Slots move will be avoided.
func (r *RedkeyClusterReconciler) doFastUpgrade(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
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
		// We need to ensure that the StatefulSet is updated before proceeding.
		// Events can happen due to the deletion of the pods and the recreation of them by the StatefulSet controller.
		if existingStatefulSet.Status.Replicas != *existingStatefulSet.Spec.Replicas {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for the StatefulSet to update")
			return true, nil
		}

		podsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Fast Upgrade", "expectedPrimaries", int(*(existingStatefulSet.Spec.Replicas)))
			return true, nil
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
		nodes, err := robin.GetClusterNodes()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
			return true, err
		}
		if len(nodes.Nodes) != redkeyCluster.NodesNeeded() {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for all cluster nodes to be updated from Robin", "robinNodes", len(nodes.Nodes), "expectedNodes", redkeyCluster.NodesNeeded())
			return true, nil
		}
		status, err := robin.GetClusterStatus()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
			return true, err
		}
		if status != redkeyv1.StatusReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster status to be Ready", "currentStatus", status)
			return true, nil
		}

		// The cluster is now upgraded, and we can set the Cluster Status as Ready again and remove the Substatus.
		redkeyCluster.Status.Status = redkeyv1.StatusReady
		redkeyCluster.Status.Substatus.Status = ""
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionUpgrading)

		return true, nil
	default:
		// Fast upgrade start: If purgeKeysOnRebalance property is set to 'true' and we have no primaries. No need to iterate over the partitions.
		// If fast upgrade is not allowed but we have no primaries, the cluster must be scaled up adding one extra
		// node to be able to move slots and keys in order to ensure keys are preserved.
		if redkeyCluster.Spec.PurgeKeysOnRebalance != nil && *redkeyCluster.Spec.PurgeKeysOnRebalance && redkeyCluster.Spec.ReplicasPerPrimary == 0 {
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

// Classic Slow Upgrade: StatefulSet updated with the config changes, cluster is scaledUp if no replicasPerPrimary set,
// slots and keys are copied. No data lose.
func (r *RedkeyClusterReconciler) doSlowUpgrade(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
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
	case redkeyv1.SubstatusResharding:
		err = r.doSlowUpgradeResharding(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusEndingSlowUpgrading:
		err = r.doSlowUpgradeEnd(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusUpgradingScalingDown:
		err = r.doSlowUpgradeScalingDown(ctx, redkeyCluster, existingStatefulSet)
	case redkeyv1.SubstatusRollingUpdate:
		err = r.doSlowUpgradeRollingUpdate(ctx, redkeyCluster, existingStatefulSet)
	default:
		err = r.doSlowUpgradeStart(ctx, redkeyCluster, existingStatefulSet)
	}

	return err
}

func (r *RedkeyClusterReconciler) doSlowUpgradeScalingUp(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Check Redis node pods rediness
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready", "primariesRequired", existingStatefulSet.Spec.Replicas, "primariesReady", existingStatefulSet.Status.ReadyReplicas)
		return err
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready", "primariesRequired", existingStatefulSet.Spec.Replicas, "primariesReady", existingStatefulSet.Status.ReadyReplicas)
		return nil // Not all pods ready -> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready", "pods", existingStatefulSet.Spec.Replicas)

	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Set the number of primaries/primariesPerPrimary to Robin to have the new node met to the existing nodes.
	primaries, replicasPerPrimary, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting primaries/replicasPerPrimary from Robin")
		return err
	}
	if primaries != int(redkeyCluster.Spec.Primaries+1) || replicasPerPrimary != int(redkeyCluster.Spec.ReplicasPerPrimary) {
		r.logInfo(redkeyCluster.NamespacedName(), "Updating Robin primaries/replicasPerPrimary", "primaries", redkeyCluster.Spec.Primaries+1, "replicasPerPrimary", redkeyCluster.Spec.ReplicasPerPrimary)
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries)+1, int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary")
		}
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Primaries+1), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin primaries/replicasPerPrimary")
			return err
		}
	}

	// Check cluster status to know if Robin has already met the new node.
	clusterStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
		return err
	}
	if clusterStatus != redkeyv1.RobinStatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
		return nil // Cluster not ready --> keep waiting
	}

	// Update substatus.
	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusResharding, "")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedkeyClusterReconciler) doSlowUpgradeResharding(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Get Robin.
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Get cluster status to know if the cluster is ready.
	clusterStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
		return err
	}
	if clusterStatus != redkeyv1.RobinStatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
		return nil // Cluster not ready --> keep waiting
	}

	// Get the current partition and update Upgrading Partition in RedkeyCluster Status if starting iterating over partitions.
	var currentPartition int
	if redkeyCluster.Status.Substatus.UpgradingPartition == "" {
		currentPartition = int(*(existingStatefulSet.Spec.Replicas)) - 1
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusResharding, strconv.Itoa(currentPartition))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else {
		currentPartition, err = strconv.Atoi(redkeyCluster.Status.Substatus.UpgradingPartition)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Upgrading Partition from RedkeyCluster object")
			return err
		}
	}

	// If first iteration over partitions: update configuration. This partition is empty: no need to move slots.
	// Else: Move slots away from partition and rolling update (don't do over the extra node to optimize).
	if currentPartition == int(*(existingStatefulSet.Spec.Replicas))-1 {
		// Update configuration: changes in configuration, labels and overrides are persisted before upgrading
		r.logInfo(redkeyCluster.NamespacedName(), "Last partition: updating configuration before rolling config")
		existingStatefulSet, err = r.upgradeClusterConfigurationUpdate(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Cluster configuration")
			return err
		}
	} else {
		// Move slots from partition before rolling update.
		r.logInfo(redkeyCluster.NamespacedName(), "Moving slots from partition before rolling config", "partition", currentPartition)
		completed, err := redkeyRobin.MoveSlots(currentPartition, currentPartition+1, 0)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error moving slots", "From node", currentPartition, "To node", currentPartition+1)
			return err
		}
		if !completed {
			r.logInfo(redkeyCluster.NamespacedName(), "Moving slots still in progress", "From node", currentPartition, "To node", currentPartition+1)
			return nil // Move slots not completed --> keep waiting
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Moving slots completed", "From node", currentPartition, "To node", currentPartition+1)
	}

	// Stop Robin reconciliation
	err = redkeyRobin.SetAndPersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.RobinStatusNoReconciling)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating/persisting Robin status", "status", redkeyv1.RobinStatusNoReconciling)
		return err
	}

	// RollingUpdate
	r.logInfo(redkeyCluster.NamespacedName(), "Executing partition Rolling Update", "partition", currentPartition)
	if currentPartition < 0 || currentPartition > math.MaxInt32 {
		err = fmt.Errorf("invalid partition index %d: must be between 0 and %d", currentPartition, int(math.MaxInt32))
		r.logError(redkeyCluster.NamespacedName(), err, "Partition value out of int32 range")
		return err
	}
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

	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusRollingUpdate, strconv.Itoa(currentPartition))
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedkeyClusterReconciler) doSlowUpgradeRollingUpdate(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	var err error

	// Check Redis node pods rediness.
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
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

	// Get the current partition
	currentPartition, err := strconv.Atoi(redkeyCluster.Status.Substatus.UpgradingPartition)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Upgrading Partition from RedkeyCluster object")
		return err
	}

	// Reset node
	err = redkeyRobin.ClusterResetNode(currentPartition)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error from Robin forgeting the node", "node index", currentPartition)
		return err
	}

	// Get cluster status to know if Robin has already resetted the node.
	clusterStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
		return err
	}
	if clusterStatus != redkeyv1.RobinStatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
		return nil // Cluster not ready --> keep waiting
	}

	// If first partition reached, we can move to the next step.
	// Else step over to the next partition.
	if currentPartition == 0 {
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingSlowUpgrading, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return err
		}
	} else {
		nextPartition := currentPartition - 1
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusResharding, strconv.Itoa(nextPartition))
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

func (r *RedkeyClusterReconciler) doSlowUpgradeEnd(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// Get Robin.
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return err
	}

	// Get cluster status to know if Robin is ready after the last rolling update.
	clusterStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
		return err
	}
	if clusterStatus != redkeyv1.RobinStatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
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
		r.logInfo(redkeyCluster.NamespacedName(), "Moving slots still in progress", "From node", extraNodeIndex, "To node", 0)
		return nil // Move slots not completed --> keep waiting
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Moving slots completed", "From node", extraNodeIndex, "To node", 0)

	err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusUpgradingScalingDown, "")
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}

	return nil
}

func (r *RedkeyClusterReconciler) doSlowUpgradeScalingDown(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin")
		return err
	}

	// Check cluster status to know if Robin has already scaled down the cluster.
	clusterStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
		return err
	}
	if clusterStatus != redkeyv1.RobinStatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
		return nil // Cluster not ready --> keep waiting
	}

	// Set the number of primaries/replicasPerPrimary to Robin to start scaling down the cluster.
	r.logInfo(redkeyCluster.NamespacedName(), "Scaling down the cluster to remove the extra node")
	primaries, replicasPerPrimary, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting primaries/replicasPerPrimary from Robin")
		return err
	}
	if primaries != int(redkeyCluster.Spec.Primaries) || replicasPerPrimary != int(redkeyCluster.Spec.ReplicasPerPrimary) {
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary")
		}
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin primaries/replicasPerPrimary")
			return err
		}
	}

	// ScaleDown the StatefulSet
	if *existingStatefulSet.Spec.Replicas > redkeyCluster.Spec.Primaries {
		r.logInfo(redkeyCluster.NamespacedName(), "Updating StatefulSet to remove the extra pod")
		*existingStatefulSet.Spec.Replicas = *existingStatefulSet.Spec.Replicas - 1
		_, err = r.updateStatefulSet(ctx, existingStatefulSet, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
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
		r.logInfo(redkeyCluster.NamespacedName(), "Cluster not yet scaled from Robin")
		return nil // Not all nodes ready --> Keep waiting
	}

	// Check cluster status from Robin.
	check, errors, warnings, err := redkeyRobin.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return err
	}
	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster readiness before ending the slow upgrade", "errors", errors, "warnings", warnings)
		return nil // Cluster not ready --> keep waiting
	}

	// Update substatus.
	redkeyCluster.Status.Status = redkeyv1.StatusReady
	redkeyCluster.Status.Substatus.Status = ""
	redkeyCluster.Status.Substatus.UpgradingPartition = ""
	setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionUpgrading)

	return nil
}

func (r *RedkeyClusterReconciler) doSlowUpgradeStart(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) error {

	// ScaleUp the cluster adding one extra node before start upgrading.
	r.logInfo(redkeyCluster.NamespacedName(), "Scaling up the cluster to add one extra node before upgrading")
	*existingStatefulSet.Spec.Replicas = *existingStatefulSet.Spec.Replicas + 1
	_, err := r.updateStatefulSet(ctx, existingStatefulSet, redkeyCluster)
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
func (r *RedkeyClusterReconciler) upgradeClusterConfigurationUpdate(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (*v1.StatefulSet, error) {
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      redkeyCluster.Name,
			Namespace: redkeyCluster.Namespace,
		},
	}

	// RedkeyCluster .Spec.Labels
	mergedLabels := redkeyCluster.GetLabels()
	defaultLabels := map[string]string{
		redis.RedkeyClusterLabel:                     redkeyCluster.Name,
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
	if redkeyCluster.Spec.Override.StatefulSet != nil && redkeyCluster.Spec.Override.StatefulSet.Spec != nil && redkeyCluster.Spec.Override.StatefulSet.Spec.Template != nil && redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Metadata.Labels != nil {
		maps.Copy(mergedLabels, redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Metadata.Labels)
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

func (r *RedkeyClusterReconciler) scaleCluster(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {

	// If a Fast Scaling is possible or already en progress, start it or check if it's already finished.
	// In both situations we return here.
	fastScaling, err := r.doFastScaling(ctx, redkeyCluster)
	if err != nil || fastScaling {
		return true, err
	}

	// Continue doing a Slow Scaling if we can't go the Fast way.
	return r.doSlowScaling(ctx, redkeyCluster)
}

func (r *RedkeyClusterReconciler) doFastScaling(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
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

		podsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Fast Scaling", "expectedPrimaries", int(*(existingStatefulSet.Spec.Replicas)))
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

		err = robin.SetAndPersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.RobinStatusScalingUp)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating/persisting Robin status", "status", redkeyv1.RobinStatusScalingUp)
			return true, err
		}

		return true, nil
	case redkeyv1.SubstatusEndingFastScaling:
		// Rebuilding the cluster after recreating all node pods. Check if the cluster is ready to end the Fast scaling.
		logger := r.getHelperLogger(redkeyCluster.NamespacedName())

		r.logInfo(redkeyCluster.NamespacedName(), "Finishing fast scaling")
		robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
			return true, err
		}
		clusterStatus, err := robin.GetClusterStatus()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
			return true, err
		}
		if clusterStatus != redkeyv1.RobinStatusReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for cluster to be Ready in Robin", "currentStatus", clusterStatus)
			return true, nil
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
		// Fast scaling start: If purgeKeysOnRebalance property is set to 'true' and we have no replicasPerPrimary set. No need to iterate over the partitions.
		if redkeyCluster.Spec.PurgeKeysOnRebalance != nil && *redkeyCluster.Spec.PurgeKeysOnRebalance && redkeyCluster.Spec.ReplicasPerPrimary == 0 {
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

			// Update Robin primaries/replicasPerPrimary.
			err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error setting Robin primaries/replicasPerPrimary", "primaries", redkeyCluster.Spec.Primaries,
					"replicas per primary", redkeyCluster.Spec.ReplicasPerPrimary)
				return true, err
			}
			err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary", "primaries", redkeyCluster.Spec.Primaries,
					"replicas per primary", redkeyCluster.Spec.ReplicasPerPrimary)
			}

			return true, nil
		}
	}

	return false, nil
}

func (r *RedkeyClusterReconciler) doSlowScaling(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
	var err error

	// By introducing primary-replica cluster, the replicas returned by statefulset (which includes replica nodes) and
	// redkeyCluster's replicas (which is just primaries) may not match if replicasPerPrimary != 0
	realExpectedNodes := int32(redkeyCluster.NodesNeeded())
	r.logInfo(redkeyCluster.NamespacedName(), "Expected cluster nodes", "Primary nodes", redkeyCluster.Spec.Primaries, "Total nodes (including replicas)", realExpectedNodes)

	sset, sset_err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if sset_err != nil {
		return true, err
	}
	currSsetReplicas := *(sset.Spec.Replicas)

	if realExpectedNodes == currSsetReplicas {
		// Complete scaling
		if redkeyCluster.Status.Status == redkeyv1.StatusScalingUp {
			immediateRequeue, err := r.completeClusterScaleUp(ctx, redkeyCluster, sset)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		} else {
			immediateRequeue, err := r.completeClusterScaleDown(ctx, redkeyCluster, sset)
			if err != nil || immediateRequeue {
				return immediateRequeue, err
			}
		}
	} else if realExpectedNodes < currSsetReplicas {
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
		err = r.scaleUpCluster(ctx, redkeyCluster, realExpectedNodes)
		if err != nil {
			return true, err
		}
	}

	// PodDisruptionBudget update
	// We use currSsetReplicas to check the configured number of primaries (not taking int account the extra pod if created)
	if redkeyCluster.Spec.Pdb.Enabled && math.Min(float64(redkeyCluster.Spec.Primaries), float64(currSsetReplicas)) > 1 {
		err = r.updatePodDisruptionBudget(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "ScaleCluster - Failed to update PodDisruptionBudget")
		} else {
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - PodDisruptionBudget updated ", "Name", redkeyCluster.Name+"-pdb")
		}
	}

	return false, nil
}

func (r *RedkeyClusterReconciler) scaleDownCluster(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {

	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	// Update node count in Robin if needed
	primaries, replicasPerPrimary, err := redkeyRobin.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin primaries/replicasPerPrimary")
		return true, err
	}
	if primaries != int(redkeyCluster.Spec.Primaries) || replicasPerPrimary != int(redkeyCluster.Spec.ReplicasPerPrimary) {
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin primaries/replicasPerPrimary", "primaries", redkeyCluster.Spec.Primaries,
				"replicas per primary", redkeyCluster.Spec.ReplicasPerPrimary)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary")
			return true, err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Robin primaries/replicasPerPrimary updated", "primaries before", primaries, "replicas per primary before", replicasPerPrimary,
			"primaries after", redkeyCluster.Spec.Primaries, "replicas per primary after", redkeyCluster.Spec.ReplicasPerPrimary)
	}

	// Check cluster node count meet the requirements
	clusterNodes, err := redkeyRobin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin nodes info")
		return true, err
	}
	existingPrimaryNodes := len(clusterNodes.GetPrimaryNodes())
	existingReplicaNodes := len(clusterNodes.GetReplicaNodes())
	expectedPrimaryNodes := int(redkeyCluster.Spec.Primaries)
	expectedReplicaNodes := int(redkeyCluster.Spec.Primaries * redkeyCluster.Spec.ReplicasPerPrimary)
	if existingPrimaryNodes != expectedPrimaryNodes || existingReplicaNodes != expectedReplicaNodes {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin to scale the cluster", "expected primary nodes", expectedPrimaryNodes,
			"existing primary nodes", existingPrimaryNodes, "expected replica nodes", expectedReplicaNodes,
			"existing replica nodes", existingReplicaNodes)
		return true, nil // Requeue
	}

	// Ensure Robin has completed resharding before scaling down the StatefulSet
	robinStatus, err := redkeyRobin.GetClusterStatus()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin status")
		return true, err
	}
	if robinStatus != robin.StatusReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin to complete resharding ", "robin status", robinStatus)
		return true, nil // Requeue
	}

	// Update StatefulSet
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		return true, err
	}
	realExpectedNodes := int32(redkeyCluster.NodesNeeded())
	sset.Spec.Replicas = &realExpectedNodes
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

func (r *RedkeyClusterReconciler) completeClusterScaleDown(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) (bool, error) {
	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusScalingPods:
		// StatefulSet has been updated with new replicas/replicasPerPrimary at this point.
		// We will ensure all pods are Ready and, then, update the new replicas/replicasPerPrimary in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
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

		if redkeyCluster.Spec.DeletePVC != nil && *redkeyCluster.Spec.DeletePVC && !redkeyCluster.Spec.Ephemeral {
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
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin")
			return true, err
		}
		status, err := robin.GetClusterStatus()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
			return true, err
		}
		if status != redkeyv1.RobinStatusReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin to end scaling down...")
			return true, nil // Cluster scaling not completed -> requeue
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Robin reports cluster is ready after scaling down")

	default:
		r.logError(redkeyCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redkeyCluster.Status.Substatus.Status)
		return true, nil
	}

	// The cluster is now scaled.
	return false, nil
}

func (r *RedkeyClusterReconciler) getCulledNodes(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) ([]robin.Node, error) {
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
		if ordinal >= int(redkeyCluster.Spec.Primaries) {
			culledNodes = append(culledNodes, node)
		}
	}

	return culledNodes, nil
}

func (r *RedkeyClusterReconciler) deleteNodePVC(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, node *robin.Node) error {
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

func (r *RedkeyClusterReconciler) scaleUpCluster(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, realExpectedReplicas int32) error {
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

func (r *RedkeyClusterReconciler) completeClusterScaleUp(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) (bool, error) {
	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	redkeyRobin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return true, err
	}

	switch redkeyCluster.Status.Substatus.Status {
	case redkeyv1.SubstatusScalingPods:
		// StatefulSet has been updated with new primaries/replicasPerPrimary at this point.
		// We will ensure all pods are Ready and, then, update the new primaries/replicasPerPrimary in Robin.

		// If not all pods ready requeue to keep waiting
		podsReady, err := r.allPodsReady(ctx, redkeyCluster, existingStatefulSet)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
			return true, err
		}
		if !podsReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for pods to become ready to end Scaling up", "expected nodes", redkeyCluster.NodesNeeded())
			return true, nil
		}

		// Update Robin with new primaries/replicasPerPrimary
		err = redkeyRobin.SetReplicas(int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating primaries/replicasPerPrimary in Robin", "primaries", redkeyCluster.Spec.Primaries, "replicasPerPrimary", redkeyCluster.Spec.ReplicasPerPrimary)
			return true, err
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary")
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
		// Robin was already updated with new primaries/replicasPerPrimary.
		// We will ensure that all cluster nodes are initialized.

		clusterNodes, err := redkeyRobin.GetClusterNodes()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
			return true, err
		}
		primaryNodes := clusterNodes.GetPrimaryNodes()
		if len(primaryNodes) != int(redkeyCluster.Spec.Primaries) {
			r.logInfo(redkeyCluster.NamespacedName(), "ScaleCluster - Inconsistency. Statefulset replicas equals to RedkeyCluster primaries but we have a different number of cluster nodes. Waiting for Robin to complete scaling up...",
				"RedkeyCluster primaries", redkeyCluster.Spec.Primaries, "Cluster nodes", primaryNodes)
		}
		err = r.updateClusterSubStatus(ctx, redkeyCluster, redkeyv1.SubstatusEndingScaling, "")
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
			return true, err
		}

		return true, nil // Cluster scaling not completed -> requeue

	case redkeyv1.SubstatusEndingScaling:
		// Final step: wait for Robin to end scaling up.

		status, err := redkeyRobin.GetClusterStatus()
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster status from Robin")
			return true, err
		}
		if status != redkeyv1.RobinStatusReady {
			r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin to end scaling up...", "robin cluster status", status)
			return true, nil // Cluster scaling not completed -> requeue
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Robin reports cluster is ready after scaling up")

	default:
		r.logError(redkeyCluster.NamespacedName(), nil, "Substatus not coherent", "substatus", redkeyCluster.Status.Substatus.Status)
		return true, nil
	}

	// The cluster is now scaled.
	return false, nil
}

func (r *RedkeyClusterReconciler) isOwnedByUs(o client.Object) bool {
	labels := o.GetLabels()
	if _, found := labels[redis.RedkeyClusterLabel]; found {
		return true
	}
	return false
}

func (r *RedkeyClusterReconciler) GetRedisSecret(redkeyCluster *redkeyv1.RedkeyCluster) (string, error) {
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

func calculateRCConfigChecksum(redkeyCluster *redkeyv1.RedkeyCluster) string {
	hash := md5.Sum([]byte(redkeyCluster.Spec.Config))
	return hex.EncodeToString(hash[:])
}

// Updates StatefulSet annotations with the config checksum (annotation "config-checksum")
func (r *RedkeyClusterReconciler) addConfigChecksumAnnotation(statefulSet *v1.StatefulSet, redkeyCluster *redkeyv1.RedkeyCluster) *v1.StatefulSet {
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
func (r *RedkeyClusterReconciler) isConfigChanged(redkeyCluster *redkeyv1.RedkeyCluster, statefulSet *v1.StatefulSet,
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
		redkeyCluster.Spec.ReplicasPerPrimary)
	observedConfig := redis.ConfigStringToMap(configMap.Data["redis.conf"])
	if !reflect.DeepEqual(observedConfig, desiredConfig) {
		return true, "Redis Config Changed - properties"
	}
	return false, ""
}
