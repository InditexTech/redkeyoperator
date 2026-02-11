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

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	redis "github.com/inditextech/redkeyoperator/internal/redis"
	"github.com/inditextech/redkeyoperator/internal/robin"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *RedkeyClusterReconciler) updateClusterStatus(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	var req reconcile.Request
	req.NamespacedName.Namespace = redkeyCluster.Namespace
	req.NamespacedName.Name = redkeyCluster.Name

	r.logInfo(redkeyCluster.NamespacedName(), "New cluster status", "status", redkeyCluster.Status.Status)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		time.Sleep(time.Second * 1)

		// Update RedkeyCluster status first

		// get a fresh redkeycluster to minimize conflicts
		refreshedRedkeyCluster := redkeyv1.RedkeyCluster{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redkeyCluster.Namespace, Name: redkeyCluster.Name}, &refreshedRedkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedRedkeyCluster.Status.Nodes = redkeyCluster.Status.Nodes
		refreshedRedkeyCluster.Status.Status = redkeyCluster.Status.Status
		refreshedRedkeyCluster.Status.Conditions = redkeyCluster.Status.Conditions
		refreshedRedkeyCluster.Status.Substatus = redkeyCluster.Status.Substatus

		err = r.Client.Status().Update(ctx, &refreshedRedkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating RedkeyCluster object with new status")
			return err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "RedkeyCluster has been updated with the new status", "status", redkeyCluster.Status.Status)

		// Update Robin status
		// Do not update if we are switching to Initializing status because Robin needs some
		// time to be ready to accept API requests.
		if redkeyCluster.Status.Status != redkeyv1.StatusInitializing && redkeyCluster.Spec.Primaries > 0 {
			logger := r.getHelperLogger(redkeyCluster.NamespacedName())
			robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to update the status")
				return err
			}

			err = robin.SetStatus(redkeyv1.GetRobinStatusCodeEquivalence(redkeyCluster.Status.Status))
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error setting the new status to Robin", "status", redkeyCluster.Status.Status)
				return err
			}
			r.logInfo(redkeyCluster.NamespacedName(), "Robin has been notified of the new status", "status", redkeyCluster.Status.Status)
		}

		// Update Robin ConfigMap status
		err = robin.PersistRobinStatus(ctx, r.Client, redkeyCluster, redkeyv1.GetRobinStatusCodeEquivalence(redkeyCluster.Status.Status))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating the new status in Robin ConfigMap", "status", redkeyCluster.Status.Status)
			return err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Robin ConfigMap has been updated with the new status", "status", redkeyCluster.Status.Status)

		return nil
	})
}

func (r *RedkeyClusterReconciler) updateClusterSubStatus(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, substatus string, partition string) error {
	refreshedRedkeyCluster := redkeyv1.RedkeyCluster{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: redkeyCluster.Namespace, Name: redkeyCluster.Name}, &refreshedRedkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
		return err
	}
	refreshedRedkeyCluster.Status.Substatus.Status = substatus
	refreshedRedkeyCluster.Status.Substatus.UpgradingPartition = partition
	redkeyCluster.Status.Substatus.Status = substatus
	redkeyCluster.Status.Substatus.UpgradingPartition = partition

	err = r.Client.Status().Update(ctx, &refreshedRedkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating substatus")
		return err
	}
	return nil
}

func (r *RedkeyClusterReconciler) updateScalingStatus(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	sset, ssetErr := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if ssetErr != nil {
		return ssetErr
	}
	currSsetReplicas := *(sset.Spec.Replicas)
	realExpectedReplicas := int32(redkeyCluster.NodesNeeded())

	logger := r.getHelperLogger(redkeyCluster.NamespacedName())

	if realExpectedReplicas < currSsetReplicas {
		redkeyCluster.Status.Status = redkeyv1.StatusScalingDown
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingUp)
		r.setConditionTrue(redkeyCluster, redkeyv1.ConditionScalingDown, fmt.Sprintf("Scaling down from %d to %d nodes", currSsetReplicas, redkeyCluster.Spec.Primaries))
	}
	if realExpectedReplicas > currSsetReplicas {
		redkeyCluster.Status.Status = redkeyv1.StatusScalingUp
		r.setConditionTrue(redkeyCluster, redkeyv1.ConditionScalingUp, fmt.Sprintf("Scaling up from %d to %d nodes", currSsetReplicas, redkeyCluster.Spec.Primaries))
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingDown)
	}
	if realExpectedReplicas == currSsetReplicas {
		if redkeyCluster.Status.Status == redkeyv1.StatusScalingDown {
			redkeyCluster.Status.Status = redkeyv1.StatusReady
			redkeyCluster.Status.Substatus.Status = ""
			setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingDown)
		}
		if redkeyCluster.Status.Status == redkeyv1.StatusScalingUp {
			if len(redkeyCluster.Status.Nodes) == int(currSsetReplicas) {
				redkeyCluster.Status.Status = redkeyv1.StatusReady
				redkeyCluster.Status.Substatus.Status = ""
				setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingUp)
			}
		}
	}

	// initialize scaling up and down conditions if they haven't been set yet
	c := meta.FindStatusCondition(redkeyCluster.Status.Conditions, redkeyv1.StatusScalingDown)
	if c == nil {
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingDown)
	}

	c = meta.FindStatusCondition(redkeyCluster.Status.Conditions, redkeyv1.StatusScalingUp)
	if c == nil {
		setConditionFalse(logger, redkeyCluster, redkeyv1.ConditionScalingUp)
	}

	return nil
}

func (r *RedkeyClusterReconciler) updateUpgradingStatus(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}}
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	if err != nil {
		return err
	}

	// Handle changes in spec.override
	_, changed := r.overrideStatefulSet(req, redkeyCluster, statefulSet)

	if changed {
		r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Override changed")
		redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
		r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Override changed")
		return nil
	}

	ns := redkeyCluster.NamespacedName()

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
			r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Previous upgrade not complete")
			redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
			return nil
		}
	}

	if redkeyCluster.Spec.Labels != nil {
		// get the actual value of labels configured in the redkeyCluster object
		desiredLabels := make(map[string]string)
		maps.Copy(desiredLabels, *redkeyCluster.Spec.Labels)

		// Add labels from override
		if redkeyCluster.Spec.Override != nil && redkeyCluster.Spec.Override.StatefulSet != nil && redkeyCluster.Spec.Override.StatefulSet.Spec != nil && redkeyCluster.Spec.Override.StatefulSet.Spec.Template != nil && redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Metadata.Labels != nil {
			for k2, v2 := range redkeyCluster.Spec.Override.StatefulSet.Spec.Template.Metadata.Labels {
				desiredLabels[k2] = v2
			}
		}

		r.logInfo(redkeyCluster.NamespacedName(), "Expected real labels configured in RedkeyCluster object", "Spec.Labels", desiredLabels)

		// get the current value of labels configured in the statefulset object
		observedLabels := statefulSet.Spec.Template.Labels
		r.logInfo(redkeyCluster.NamespacedName(), "Current statefulset configuration", "Spec.Template.Labels", observedLabels)

		// Validate if were added new labels
		for key, value := range desiredLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			val, exists := statefulSet.Spec.Template.Labels[key]
			if !exists {
				r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
				r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			} else {
				if value != val {
					r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
					redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
					r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Redis Labels Changed")
					return nil
				}
			}
		}

		defaultLabels := map[string]string{
			redis.RedkeyClusterLabel:                     redkeyCluster.Name,
			r.getStatefulSetSelectorLabel(redkeyCluster): "redis",
		}

		// add defaultLabels
		maps.Copy(desiredLabels, defaultLabels)

		// Validate if  were deleted existing labels
		for key := range observedLabels {
			// check if key exists in the map element
			// and get the value from spec template statefulset
			_, exists := desiredLabels[key]

			if !exists {
				r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Labels Changed", "observed", observedLabels, "desired", desiredLabels)
				redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
				r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Redis Labels Changed")
				return nil
			}
		}
	}

	// Check whether the redis configuration has changed,
	// as this will constitute a cluster upgrade
	configChanged, reason := r.isConfigChanged(redkeyCluster, statefulSet, configMap)
	if configChanged {
		r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", reason)
		redkeyCluster.Status.Status = redkeyv1.StatusUpgrading
		r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Configuration in redis.conf is being updated")
		return nil
	}

	if redkeyCluster.Spec.Resources != nil {
		// Check whether any upgradeable object changes have been made.
		// Any change in these attributes or properties will constitute a live upgrade.
		desiredResources := *(redkeyCluster.Spec.Resources)
		observedResources := statefulSet.Spec.Template.Spec.Containers[0].Resources

		// We need to individually compare the resources we care about,
		// as DeepEqual gets it wrong for CPU quantities
		if !reflect.DeepEqual(observedResources.Limits.Cpu().String(), desiredResources.Limits.Cpu().String()) ||
			!reflect.DeepEqual(observedResources.Limits.Memory().String(), desiredResources.Limits.Memory().String()) ||
			!reflect.DeepEqual(observedResources.Requests.Cpu().String(), desiredResources.Requests.Cpu().String()) ||
			!reflect.DeepEqual(observedResources.Requests.Memory().String(), desiredResources.Requests.Memory().String()) {
			r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Resource Requests & Limits Changed", "observed", observedResources, "desired", desiredResources)

			redkeyCluster.Status.Status = redkeyv1.StatusUpgrading

			r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, "Redis Resource Requests & Limits Changed")

			return nil
		}
	}

	// Check whether any upgradeable object changes have been made.
	// Any change in these attributes or properties will constitute a live upgrade.
	desiredImage := redkeyCluster.Spec.Image
	observedImage := statefulSet.Spec.Template.Spec.Containers[0].Image
	if observedImage != desiredImage {
		r.logInfo(redkeyCluster.NamespacedName(), "Cluster Upgrade Issued", "reason", "Redis Image Changed", "observed", observedImage, "desired", desiredImage)
		redkeyCluster.Status.Status = redkeyv1.StatusUpgrading

		r.setConditionTrue(redkeyCluster, redkeyv1.ConditionUpgrading, fmt.Sprintf("Redis Image Changed: observed %s desired %s", observedImage, desiredImage))

		return nil
	}

	redkeyCluster.Status.Status = redkeyv1.StatusReady
	redkeyCluster.Status.Substatus.Status = ""

	c := meta.FindStatusCondition(redkeyCluster.Status.Conditions, redkeyv1.StatusUpgrading)
	if c != nil && c.Status == metav1.ConditionTrue {
		setConditionFalse(r.getHelperLogger(redkeyCluster.NamespacedName()), redkeyCluster, redkeyv1.ConditionUpgrading)
	}

	return nil
}
