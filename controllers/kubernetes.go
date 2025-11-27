// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"reflect"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/kubernetes"
	redis "github.com/inditextech/redkeyoperator/internal/redis"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *RedkeyClusterReconciler) checkAndCreateK8sObjects(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
	var immediateRequeue bool = false
	var err error = nil
	var configMap *corev1.ConfigMap

	// RedkeyCluster check
	err = r.checkAndUpdateRKCL(ctx, req.Name, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking RedkeyCluster object")
		return immediateRequeue, err
	}

	// ConfigMap check
	if configMap, immediateRequeue, err = r.checkAndCreateConfigMap(ctx, req, redkeyCluster); err != nil {
		return immediateRequeue, err
	}

	// PodDisruptionBudget check
	r.checkAndManagePodDisruptionBudget(ctx, req, redkeyCluster)

	// StatefulSet check
	if immediateRequeue, err = r.checkAndCreateStatefulSet(ctx, req, redkeyCluster, configMap); err != nil {
		return immediateRequeue, err
	}

	// Robin deployment check
	r.checkAndCreateRobin(ctx, req, redkeyCluster)

	// Service check
	immediateRequeue, err = r.checkAndCreateService(ctx, req, redkeyCluster)

	return immediateRequeue, err
}

func (r *RedkeyClusterReconciler) checkAndUpdateRKCL(ctx context.Context, redisName string, redkeyCluster *redkeyv1.RedkeyCluster) error {
	// Label redis.RedkeyClusterLabel needed by Redis backup
	if _, ok := redkeyCluster.Labels[redis.RedkeyClusterLabel]; !ok {
		r.logInfo(redkeyCluster.NamespacedName(), "RKCL object not containing label", "label", redis.RedkeyClusterLabel)
		redkeyCluster.ObjectMeta.Labels[redis.RedkeyClusterLabel] = redisName
		if err := r.Update(ctx, redkeyCluster); err != nil {
			return err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Label added to RedkeyCluster labels", "label", redis.RedkeyClusterLabel, "value", redisName)
	}
	// Label redis.RedkeyClusterComponentLabel needed by Redis backup
	if _, ok := redkeyCluster.Labels[redis.RedkeyClusterComponentLabel]; !ok {
		r.logInfo(redkeyCluster.NamespacedName(), "RKCL object not containing label", "label", redis.RedkeyClusterComponentLabel)
		redkeyCluster.ObjectMeta.Labels[redis.RedkeyClusterComponentLabel] = "redis"
		if err := r.Update(ctx, redkeyCluster); err != nil {
			return err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "Label added to RedkeyCluster labels", "label", redis.RedkeyClusterComponentLabel, "value", "redis")
	}
	return nil
}

func (r *RedkeyClusterReconciler) checkAndCreateConfigMap(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) (*corev1.ConfigMap, bool, error) {
	var immediateRequeue = false
	var auth = &corev1.Secret{}
	var err error

	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		if errors.IsNotFound(err) {
			if len(redkeyCluster.Spec.Auth.SecretName) > 0 {
				auth, err = r.getSecret(ctx, types.NamespacedName{
					Name:      redkeyCluster.Spec.Auth.SecretName,
					Namespace: req.Namespace,
				}, redkeyCluster.NamespacedName())
				if err != nil {
					r.logError(redkeyCluster.NamespacedName(), err, "Can't find provided secret", "redkeyCluster", redkeyCluster)
					return nil, immediateRequeue, err
				}
			}
			configMap = r.createConfigMap(req, redkeyCluster.Spec, auth, redkeyCluster.GetObjectMeta().GetLabels())
			ctrl.SetControllerReference(redkeyCluster, configMap, r.Scheme)
			r.logInfo(redkeyCluster.NamespacedName(), "Creating configmap", "configmap", configMap.Name)
			createMapErr := r.Client.Create(ctx, configMap)
			if createMapErr != nil {
				r.logError(redkeyCluster.NamespacedName(), createMapErr, "Error when creating configmap")
				return nil, immediateRequeue, createMapErr
			}
		} else {
			r.logError(redkeyCluster.NamespacedName(), err, "Getting configmap data failed")
			return nil, immediateRequeue, err
		}
	}
	return configMap, immediateRequeue, nil
}

func (r *RedkeyClusterReconciler) getSecret(ctx context.Context, ns types.NamespacedName, RCNamespacedName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, ns, secret)
	if err != nil {
		r.logError(RCNamespacedName, err, "Getting secret failed", "secret", ns)
	}
	return secret, err
}

func (r *RedkeyClusterReconciler) createConfigMap(req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, secret *corev1.Secret, labels map[string]string) *corev1.ConfigMap {
	newRedkeyClusterConf := redis.ConfigStringToMap(spec.Config)
	labels[redis.RedkeyClusterLabel] = req.Name
	labels[redis.RedkeyClusterComponentLabel] = "redis"
	if val, exists := secret.Data["requirepass"]; exists {
		newRedkeyClusterConf["requirepass"] = append(newRedkeyClusterConf["requirepass"], string(val))

	} else if secret.Name != "" {
		r.logInfo(req.NamespacedName, "requirepass field not found in secret", "secretdata", secret.Data)
	}

	redisConfMap := redis.MergeWithDefaultConfig(newRedkeyClusterConf, spec.Ephemeral, spec.ReplicasPerPrimary)

	redisConf := redis.MapToConfigString(redisConfMap)
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"redis.conf": redisConf},
	}

	r.logInfo(req.NamespacedName, "Generated Configmap", "configmap", cm)
	r.logInfo(req.NamespacedName, "Spec config", "speconfig", spec.Config)
	return &cm
}

func (r *RedkeyClusterReconciler) checkAndCreateStatefulSet(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, configMap *corev1.ConfigMap) (bool, error) {
	var immediateRequeue = false
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	var createSsetError error
	if err != nil {
		if errors.IsNotFound(err) {
			// Create StatefulSet
			r.logInfo(redkeyCluster.NamespacedName(), "Creating statefulset")
			statefulSet, createSsetError = r.createStatefulSet(ctx, req, redkeyCluster.Spec, *redkeyCluster.Spec.Labels, configMap)
			if createSsetError != nil {
				r.logError(redkeyCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				return immediateRequeue, createSsetError
			}
			r.logInfo(redkeyCluster.NamespacedName(), "Successfully created statefulset")

			// Set the config checksum annotation
			statefulSet = r.addConfigChecksumAnnotation(statefulSet, redkeyCluster)

			ctrl.SetControllerReference(redkeyCluster, statefulSet, r.Scheme)
			createSsetError = r.Client.Create(ctx, statefulSet)
			if createSsetError != nil {
				if errors.IsAlreadyExists(createSsetError) {
					r.logInfo(redkeyCluster.NamespacedName(), "StatefulSet already exists")
				} else {
					r.logError(redkeyCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				}
			}
		} else {
			r.logError(redkeyCluster.NamespacedName(), err, "Getting statefulset data failed", "statefulset", statefulSet.Name)
			return immediateRequeue, err
		}
	} else {
		// Check StatefulSet <-> RedkeyCluster coherence

		// When scaling up before upgrading we can have inconsistencies currReadyNodes <> currSsetReplicas
		// we skip these checks till the sacling up is done.
		if redkeyCluster.Status.Status == redkeyv1.StatusUpgrading && redkeyCluster.Status.Substatus.Status == redkeyv1.SubstatusUpgradingScalingUp {
			return immediateRequeue, nil
		}

		currSsetReplicas := *(statefulSet.Spec.Replicas)
		realExpectedReplicas := int32(redkeyCluster.NodesNeeded())
		immediateRequeue = false
		var err error
		if redkeyCluster.Status.Status == "" || redkeyCluster.Status.Status == redkeyv1.StatusInitializing {
			if currSsetReplicas != realExpectedReplicas {
				immediateRequeue = true
				r.logInfo(redkeyCluster.NamespacedName(), "Replicas updated before reaching Configuring status: aligning StatefulSet <-> RedkeyCluster replicas",
					"StatefulSet replicas", currSsetReplicas, "RedkeyCluster replicas", realExpectedReplicas)
				statefulSet.Spec.Replicas = &realExpectedReplicas
				_, err = r.updateStatefulSet(ctx, statefulSet, redkeyCluster)
				if err != nil {
					r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
				}
				return immediateRequeue, err
			}
		}
		// if realExpectedReplicas < currSsetReplicas {
		// 	// Inconsistency: if a scaleup could not be completed because of a lack of resources that prevented
		// 	// all the needed pods from being created
		// 	// StatefulSet replicas are then aligned with RedkeyCluster replicas
		// 	r.logInfo(redkeyCluster.NamespacedName(), "Not all required pods instantiated: aligning StatefulSet <-> RedkeyCluster replicas",
		// 		"StatefulSet replicas", currSsetReplicas, "RedkeyCluster replicas", realExpectedReplicas)
		// 	statefulSet.Spec.Replicas = &realExpectedReplicas
		// 	_, err = r.updateStatefulSet(ctx, statefulSet, redkeyCluster)
		// 	if err != nil {
		// 		r.logError(redkeyCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
		// 	}
		// }
		return immediateRequeue, err
	}
	return immediateRequeue, nil
}

func (r *RedkeyClusterReconciler) createStatefulSet(ctx context.Context, req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, labels map[string]string, configmap *corev1.ConfigMap) (*v1.StatefulSet, error) {
	statefulSet, err := redis.CreateStatefulSet(ctx, req, spec, labels)
	if err != nil {
		return statefulSet, err
	}

	// Add labels to statefulset and its template if provided
	if spec.Labels != nil {
		for k, v := range *spec.Labels {
			statefulSet.Labels[k] = v
			statefulSet.Spec.Template.Labels[k] = v
		}
	}

	// Set resources if provided
	inferResources := true
	if spec.Resources != nil {
		inferResources = false
		for k := range statefulSet.Spec.Template.Spec.Containers {
			statefulSet.Spec.Template.Spec.Containers[k].Resources = *spec.Resources
		}
	}

	// Override the statefulset with the provided override
	if spec.Override != nil && spec.Override.StatefulSet != nil {
		patchedStatefulSet, err := redis.ApplyStsOverride(statefulSet, spec.Override.StatefulSet)
		if err != nil {
			return nil, err
		}
		statefulSet = patchedStatefulSet

		// Check if the override has resources
		if len(spec.Override.StatefulSet.Spec.Template.Spec.Containers) > 0 && (spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Resources.Requests != nil || spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Resources.Limits != nil) {
			inferResources = false
		}
	}

	// Infer resources if needed
	if inferResources {
		statefulSetWithInferredResources, err := r.inferResources(req, spec, configmap, statefulSet)
		if err != nil {
			return nil, err
		}
		statefulSet = statefulSetWithInferredResources
	}

	return statefulSet, nil
}

func (r *RedkeyClusterReconciler) inferResources(req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, configmap *corev1.ConfigMap, statefulSet *v1.StatefulSet) (*v1.StatefulSet, error) {
	config := spec.Config
	desiredConfig := redis.MergeWithDefaultConfig(
		redis.ConfigStringToMap(config),
		spec.Ephemeral,
		spec.ReplicasPerPrimary)

	maxMemoryInt, err := redis.ExtractMaxMemory(desiredConfig)
	if err != nil {
		return nil, err
	}

	r.logInfo(req.NamespacedName, "Merged config", "withDefaults", desiredConfig)

	memoryOverheadConfig := configmap.Data["maxmemory-overhead"]
	var memoryOverheadResource resource.Quantity

	if memoryOverheadConfig == "" {
		memoryOverheadResource = resource.MustParse("300Mi")
	} else {
		memoryOverheadResource = resource.MustParse(memoryOverheadConfig)
	}

	memoryLimit, _ := resource.ParseQuantity(fmt.Sprintf("%dMi", maxMemoryInt)) // add 300 mb from config maxmemory
	cpuLimit, _ := resource.ParseQuantity("1")
	r.logInfo(req.NamespacedName, "New memory limits", "memory", memoryLimit)
	memoryLimit.Add(memoryOverheadResource)
	for k := range statefulSet.Spec.Template.Spec.Containers {
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceMemory] = memoryLimit
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory] = memoryLimit
		r.logInfo(req.NamespacedName, "Stateful set container memory", "memory", statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory])

		statefulSet.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceCPU] = cpuLimit
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU] = cpuLimit
		r.logInfo(req.NamespacedName, "Stateful set cpu", "cpu", statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU])

	}
	return statefulSet, nil
}

func (r *RedkeyClusterReconciler) overrideStatefulSet(req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, statefulSet *v1.StatefulSet) (*v1.StatefulSet, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redkeyCluster.Spec.Override == nil {
		redkeyCluster.Spec.Override = &redkeyv1.RedkeyClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redkeyCluster.Spec.Override.StatefulSet == nil {
		redkeyCluster.Spec.Override.StatefulSet = &v1.StatefulSet{}
	}

	// Apply the override
	patchedStatefulSet, err := redis.ApplyStsOverride(statefulSet, redkeyCluster.Spec.Override.StatefulSet)
	if err != nil {
		ctrl.Log.Error(err, "Error applying StatefulSet override")
		return statefulSet, false
	}

	// Apply the resources override if provided in containers without resources after patch
	if redkeyCluster.Spec.Resources != nil {
		for k := range patchedStatefulSet.Spec.Template.Spec.Containers {
			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits = redkeyCluster.Spec.Resources.Limits
			}

			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests = redkeyCluster.Spec.Resources.Requests
			}
		}
	}

	// Check if the override changes something in the original StatefulSet
	changed := !reflect.DeepEqual(statefulSet.Labels, patchedStatefulSet.Labels) || !reflect.DeepEqual(statefulSet.Annotations, patchedStatefulSet.Annotations) || !reflect.DeepEqual(statefulSet.Spec, patchedStatefulSet.Spec)

	if changed {
		r.logInfo(req.NamespacedName, "Detected StatefulSet override change")
	} else {
		r.logInfo(req.NamespacedName, "No StatefulSet override change detected")
	}

	return patchedStatefulSet, changed
}

func (r *RedkeyClusterReconciler) checkAndCreateService(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.logError(redkeyCluster.NamespacedName(), err, "Getting svc data failed")
			return false, err
		}

		// Create service otherwise
		r.logInfo(redkeyCluster.NamespacedName(), "Creating service")
		service := r.createService(req, redkeyCluster.Spec, redkeyCluster.GetObjectMeta().GetLabels())
		ctrl.SetControllerReference(redkeyCluster, service, r.Scheme)

		createSVCError := r.Client.Create(ctx, service)
		if createSVCError != nil {
			if !errors.IsAlreadyExists(createSVCError) {
				return false, createSVCError
			}

			r.logInfo(redkeyCluster.NamespacedName(), "Svc already exists")
			return false, nil
		}

		r.logInfo(redkeyCluster.NamespacedName(), "Successfully created service", "service", service.Name)
	} else {
		// Handle changes in spec.override.service
		patchedService, changed := r.overrideService(req, redkeyCluster, service)

		// Update service if changed
		if changed {
			service = patchedService
			err := r.Client.Update(ctx, service)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Error updating service")
				return false, err
			}
			r.logInfo(redkeyCluster.NamespacedName(), "Successfully updated service", "service", service.Name)
		}
	}
	return false, nil
}

func (r *RedkeyClusterReconciler) createService(req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, labels map[string]string) *corev1.Service {
	service := redis.CreateService(req.Namespace, req.Name, labels)

	// Override the service with the provided override
	if spec.Override != nil && spec.Override.Service != nil {
		patchedService, err := redis.ApplyServiceOverride(service, spec.Override.Service)
		if err != nil {
			return service
		}
		service = patchedService
	}

	return service
}

func (r *RedkeyClusterReconciler) overrideService(req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, service *corev1.Service) (*corev1.Service, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redkeyCluster.Spec.Override == nil {
		redkeyCluster.Spec.Override = &redkeyv1.RedkeyClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redkeyCluster.Spec.Override.Service == nil {
		redkeyCluster.Spec.Override.Service = &corev1.Service{}
	}

	// Apply the override
	patchedService, err := redis.ApplyServiceOverride(service, redkeyCluster.Spec.Override.Service)
	if err != nil {
		ctrl.Log.Error(err, "Error applying Service override")
		return service, false
	}

	// Check if the override changes something in the original Service
	changed := !reflect.DeepEqual(service.Labels, patchedService.Labels) || !reflect.DeepEqual(service.Annotations, patchedService.Annotations) || !reflect.DeepEqual(service.Spec, patchedService.Spec)

	if changed {
		r.logInfo(req.NamespacedName, "Detected service override change")
	}

	return patchedService, changed
}

func (r *RedkeyClusterReconciler) allPodsReady(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster, existingStatefulSet *v1.StatefulSet) (bool, error) {
	listOptions := client.ListOptions{
		Namespace: redkeyCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedkeyClusterLabel:                     redkeyCluster.Name,
				r.getStatefulSetSelectorLabel(redkeyCluster): "redis",
			},
		),
	}
	podsReady, err := kubernetes.AllPodsReady(ctx, r.Client, &listOptions, int(*existingStatefulSet.Spec.Replicas))
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for pods being ready")
		return false, err
	}
	return podsReady, nil
}

func (r *RedkeyClusterReconciler) getPersistentVolumeClaim(ctx context.Context, client client.Client, redkeyCluster *redkeyv1.RedkeyCluster, name string) (*corev1.PersistentVolumeClaim, error) {
	r.logInfo(redkeyCluster.NamespacedName(), "Getting persistent volume claim to be deleted ")
	pvc := &corev1.PersistentVolumeClaim{}
	err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: redkeyCluster.Namespace}, pvc)

	if err != nil {
		return nil, err
	}
	return pvc, nil
}

func (ef *RedkeyClusterReconciler) deletePVC(ctx context.Context, client client.Client, pvc *corev1.PersistentVolumeClaim) error {
	err := client.Delete(ctx, pvc)
	return err
}

// GetStatefulSetSelectorLabel returns the label key that should be used to find RedkeyCluster nodes for
// backwards compatibility with the old version RedkeyCluster.
// The implementation has been moved, and this exists merely as an alias for backward compatibility
func (r *RedkeyClusterReconciler) getStatefulSetSelectorLabel(rkcl *redkeyv1.RedkeyCluster) string {
	return kubernetes.GetStatefulSetSelectorLabel(context.TODO(), r.Client, rkcl)
}

func (r *RedkeyClusterReconciler) updateStatefulSet(ctx context.Context, statefulSet *v1.StatefulSet, redkeyCluster *redkeyv1.RedkeyCluster) (*v1.StatefulSet, error) {
	refreshedStatefulSet := &v1.StatefulSet{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh redkeycluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: statefulSet.Namespace, Name: statefulSet.Name}, refreshedStatefulSet)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
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

func (r *RedkeyClusterReconciler) updateDeployment(ctx context.Context, deployment *v1.Deployment, redkeyCluster *redkeyv1.RedkeyCluster) (*v1.Deployment, error) {
	refreshedDeployment := &v1.Deployment{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh redkeycluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, refreshedDeployment)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
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
