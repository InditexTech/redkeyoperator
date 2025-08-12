// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	redis "github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/robin"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *RedisClusterReconciler) updateClusterStatus(ctx context.Context, redisCluster *redisv1.RedKeyCluster) error {
	var req reconcile.Request
	req.NamespacedName.Namespace = redisCluster.Namespace
	req.NamespacedName.Name = redisCluster.Name

	r.logInfo(redisCluster.NamespacedName(), "New cluster status", "status", redisCluster.Status.Status)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		time.Sleep(time.Second * 1)

		// Update RedisCluster status first

		// get a fresh rediscluster to minimize conflicts
		refreshedRedisCluster := redisv1.RedKeyCluster{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedRedisCluster.Status.Nodes = redisCluster.Status.Nodes
		refreshedRedisCluster.Status.Status = redisCluster.Status.Status
		refreshedRedisCluster.Status.Conditions = redisCluster.Status.Conditions
		refreshedRedisCluster.Status.Substatus = redisCluster.Status.Substatus

		err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating RedisCluster object with new status")
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "RedisCluster has been updated with the new status", "status", redisCluster.Status.Status)

		// Update Robin status
		// Do not update if we are switching to Initializing status because Robin needs some
		// time to be ready to accept API requests.
		if redisCluster.Status.Status != redisv1.StatusInitializing {
			logger := r.getHelperLogger(redisCluster.NamespacedName())
			robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to update the status")
				return err
			}

			err = robin.SetStatus(redisCluster.Status.Status)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error setting the new status to Robin", "status", redisCluster.Status.Status)
				return err
			}
			r.logInfo(redisCluster.NamespacedName(), "Robin has been notified of the new status", "status", redisCluster.Status.Status)
		}

		// Update Robin ConfigMap status
		err = robin.PersistRobinStatut(ctx, r.Client, redisCluster, redisCluster.Status.Status)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating the new status in Robin ConfigMap", "status", redisCluster.Status.Status)
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Robin ConfigMap has been updated with the new status", "status", redisCluster.Status.Status)

		return nil
	})
}

func (r *RedisClusterReconciler) updateClusterSubStatus(ctx context.Context, redisCluster *redisv1.RedKeyCluster, substatus string, partition string) error {
	refreshedRedisCluster := redisv1.RedKeyCluster{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
		return err
	}
	refreshedRedisCluster.Status.Substatus.Status = substatus
	refreshedRedisCluster.Status.Substatus.UpgradingPartition = partition
	redisCluster.Status.Substatus.Status = substatus
	redisCluster.Status.Substatus.UpgradingPartition = partition

	err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}
	return nil
}

func (r *RedisClusterReconciler) updateScalingStatus(ctx context.Context, redisCluster *redisv1.RedKeyCluster) error {
	sset, ssetErr := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if ssetErr != nil {
		return ssetErr
	}
	currSsetReplicas := *(sset.Spec.Replicas)
	realExpectedReplicas := int32(redisCluster.NodesNeeded())

	logger := r.getHelperLogger(redisCluster.NamespacedName())

	if realExpectedReplicas < currSsetReplicas {
		redisCluster.Status.Status = redisv1.StatusScalingDown
		setConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
		r.setConditionTrue(redisCluster, redisv1.ConditionScalingDown, fmt.Sprintf("Scaling down from %d to %d nodes", currSsetReplicas, redisCluster.Spec.Replicas))
	}
	if realExpectedReplicas > currSsetReplicas {
		redisCluster.Status.Status = redisv1.StatusScalingUp
		r.setConditionTrue(redisCluster, redisv1.ConditionScalingUp, fmt.Sprintf("Scaling up from %d to %d nodes", currSsetReplicas, redisCluster.Spec.Replicas))
		setConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
	}
	if realExpectedReplicas == currSsetReplicas {
		if redisCluster.Status.Status == redisv1.StatusScalingDown {
			redisCluster.Status.Status = redisv1.StatusReady
			redisCluster.Status.Substatus.Status = ""
			setConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
		}
		if redisCluster.Status.Status == redisv1.StatusScalingUp {
			if len(redisCluster.Status.Nodes) == int(currSsetReplicas) {
				redisCluster.Status.Status = redisv1.StatusReady
				redisCluster.Status.Substatus.Status = ""
				setConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
			}
		}
	}

	// initialize scaling up and down conditions if they haven't been set yet
	c := meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusScalingDown)
	if c == nil {
		setConditionFalse(logger, redisCluster, redisv1.ConditionScalingDown)
	}

	c = meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusScalingUp)
	if c == nil {
		setConditionFalse(logger, redisCluster, redisv1.ConditionScalingUp)
	}

	return nil
}

func (r *RedisClusterReconciler) updateUpgradingStatus(ctx context.Context, redisCluster *redisv1.RedKeyCluster) error {
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}}
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}

	// Handle changes in spec.override
	_, changed := r.overrideStatefulSet(req, redisCluster, statefulSet)

	if changed {
		r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Override changed")
		redisCluster.Status.Status = redisv1.StatusUpgrading
		r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Override changed")
		return nil
	}

	ns := redisCluster.NamespacedName()

	configMap, err := r.FindExistingConfigMapFunc(ctx, ctrl.Request{NamespacedName: ns})
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
			r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Previous upgrade not complete")
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

		r.logInfo(redisCluster.NamespacedName(), "Expected real labels configured in RedisCluster object", "Spec.Labels", desiredLabels)

		// get the current value of labels configured in the statefulset object
		observedLabels := statefulSet.Spec.Template.Labels
		r.logInfo(redisCluster.NamespacedName(), "Current statefulset configuration", "Spec.Template.Labels", observedLabels)

		// Validate if were added new labels
		for key, value := range desiredLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			val, exists := statefulSet.Spec.Template.Labels[key]
			if !exists {
				r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusUpgrading
				r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			} else {
				if value != val {
					r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
					redisCluster.Status.Status = redisv1.StatusUpgrading
					r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
					return nil
				}
			}
		}

		defaultLabels := map[string]string{
			redis.RedisClusterLabel:                     redisCluster.Name,
			r.getStatefulSetSelectorLabel(redisCluster): "redis",
		}

		// add defaultLabels
		maps.Copy(desiredLabels, defaultLabels)

		// Validate if  were deleted existing labels
		for key := range observedLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			_, exists := desiredLabels[key]

			if !exists {
				r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusUpgrading
				r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			}
		}
	}

	// Check whether the redis configuration has changed,
	// as this will constitute a cluster upgrade
	configChanged, reason := r.isConfigChanged(redisCluster, statefulSet, configMap)
	if configChanged {
		r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", reason)
		redisCluster.Status.Status = redisv1.StatusUpgrading
		r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Configuration in redis.conf is being updated")
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
			r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Resource Requests & Limits Changed", "observed", observedResources, "desired", desiredResources)

			redisCluster.Status.Status = redisv1.StatusUpgrading

			r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, "Redis Resource Requests & Limits Changed")

			return nil
		}
	}

	// Check whether any upgradeable object changes have been made.
	// Any change in these attributes or properties will constitute a live upgrade.
	desiredImage := redisCluster.Spec.Image
	observedImage := statefulSet.Spec.Template.Spec.Containers[0].Image
	if observedImage != desiredImage {
		r.logInfo(redisCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Image Changed", "observed", observedImage, "desired", desiredImage)
		redisCluster.Status.Status = redisv1.StatusUpgrading

		r.setConditionTrue(redisCluster, redisv1.ConditionUpgrading, fmt.Sprintf("Redis Image Changed: observed %s desired %s", observedImage, desiredImage))

		return nil
	}

	redisCluster.Status.Status = redisv1.StatusReady
	redisCluster.Status.Substatus.Status = ""

	c := meta.FindStatusCondition(redisCluster.Status.Conditions, redisv1.StatusUpgrading)
	if c != nil && c.Status == metav1.ConditionTrue {
		setConditionFalse(r.getHelperLogger(redisCluster.NamespacedName()), redisCluster, redisv1.ConditionUpgrading)
	}

	return nil
}
