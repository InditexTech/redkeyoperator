// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"reflect"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	redis "github.com/inditextech/redisoperator/internal/redis"
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

func (r *RedisClusterReconciler) checkAndCreateK8sObjects(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedKeyCluster) (bool, error) {
	var immediateRequeue bool = false
	var err error = nil
	var configMap *corev1.ConfigMap

	// RedisCluster check
	err = r.checkAndUpdateRDCL(ctx, req.Name, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error checking RedisCluster object")
		return immediateRequeue, err
	}

	// ConfigMap check
	if configMap, immediateRequeue, err = r.checkAndCreateConfigMap(ctx, req, redisCluster); err != nil {
		return immediateRequeue, err
	}

	// PodDisruptionBudget check
	r.checkAndManagePodDisruptionBudget(ctx, req, redisCluster)

	// StatefulSet check
	if immediateRequeue, err = r.checkAndCreateStatefulSet(ctx, req, redisCluster, configMap); err != nil {
		return immediateRequeue, err
	}

	// Robin deployment check
	r.checkAndCreateRobin(ctx, req, redisCluster)

	// Service check
	immediateRequeue, err = r.checkAndCreateService(ctx, req, redisCluster)

	return immediateRequeue, err
}

func (r *RedisClusterReconciler) checkAndUpdateRDCL(ctx context.Context, redisName string, redisCluster *redisv1.RedKeyCluster) error {
	// Label redis.RedisClusterLabel needed by Redis backup
	if _, ok := redisCluster.Labels[redis.RedisClusterLabel]; !ok {
		r.logInfo(redisCluster.NamespacedName(), "RDCL object not containing label", "label", redis.RedisClusterLabel)
		redisCluster.ObjectMeta.Labels[redis.RedisClusterLabel] = redisName
		if err := r.Update(ctx, redisCluster); err != nil {
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Label added to RedisCluster labels", "label", redis.RedisClusterLabel, "value", redisName)
	}
	// Label redis.RedisClusterComponentLabel needed by Redis backup
	if _, ok := redisCluster.Labels[redis.RedisClusterComponentLabel]; !ok {
		r.logInfo(redisCluster.NamespacedName(), "RDCL object not containing label", "label", redis.RedisClusterComponentLabel)
		redisCluster.ObjectMeta.Labels[redis.RedisClusterComponentLabel] = "redis"
		if err := r.Update(ctx, redisCluster); err != nil {
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Label added to RedisCluster labels", "label", redis.RedisClusterComponentLabel, "value", "redis")
	}
	return nil
}

func (r *RedisClusterReconciler) checkAndCreateConfigMap(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedKeyCluster) (*corev1.ConfigMap, bool, error) {
	var immediateRequeue = false
	var auth = &corev1.Secret{}
	var err error

	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		if errors.IsNotFound(err) {
			if len(redisCluster.Spec.Auth.SecretName) > 0 {
				auth, err = r.getSecret(ctx, types.NamespacedName{
					Name:      redisCluster.Spec.Auth.SecretName,
					Namespace: req.Namespace,
				}, redisCluster.NamespacedName())
				if err != nil {
					r.logError(redisCluster.NamespacedName(), err, "Can't find provided secret", "redisCluster", redisCluster)
					return nil, immediateRequeue, err
				}
			}
			configMap = r.createConfigMap(req, redisCluster.Spec, auth, redisCluster.GetObjectMeta().GetLabels())
			ctrl.SetControllerReference(redisCluster, configMap, r.Scheme)
			r.logInfo(redisCluster.NamespacedName(), "Creating configmap", "configmap", configMap.Name)
			createMapErr := r.Client.Create(ctx, configMap)
			if createMapErr != nil {
				r.logError(redisCluster.NamespacedName(), createMapErr, "Error when creating configmap")
				return nil, immediateRequeue, createMapErr
			}
		} else {
			r.logError(redisCluster.NamespacedName(), err, "Getting configmap data failed")
			return nil, immediateRequeue, err
		}
	}
	return configMap, immediateRequeue, nil
}

func (r *RedisClusterReconciler) getSecret(ctx context.Context, ns types.NamespacedName, RCNamespacedName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, ns, secret)
	if err != nil {
		r.logError(RCNamespacedName, err, "Getting secret failed", "secret", ns)
	}
	return secret, err
}

func (r *RedisClusterReconciler) createConfigMap(req ctrl.Request, spec redisv1.RedKeyClusterSpec, secret *corev1.Secret, labels map[string]string) *corev1.ConfigMap {
	newRedisClusterConf := redis.ConfigStringToMap(spec.Config)
	labels[redis.RedisClusterLabel] = req.Name
	labels[redis.RedisClusterComponentLabel] = "redis"
	if val, exists := secret.Data["requirepass"]; exists {
		newRedisClusterConf["requirepass"] = append(newRedisClusterConf["requirepass"], string(val))

	} else if secret.Name != "" {
		r.logInfo(req.NamespacedName, "requirepass field not found in secret", "secretdata", secret.Data)
	}

	redisConfMap := redis.MergeWithDefaultConfig(newRedisClusterConf, spec.Ephemeral, spec.ReplicasPerMaster)

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

func (r *RedisClusterReconciler) checkAndCreateStatefulSet(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedKeyCluster, configMap *corev1.ConfigMap) (bool, error) {
	var immediateRequeue = false
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	var createSsetError error
	if err != nil {
		if errors.IsNotFound(err) {
			// Create StatefulSet
			r.logInfo(redisCluster.NamespacedName(), "Creating statefulset")
			statefulSet, createSsetError = r.createStatefulSet(ctx, req, redisCluster.Spec, *redisCluster.Spec.Labels, configMap)
			if createSsetError != nil {
				r.logError(redisCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				return immediateRequeue, createSsetError
			}
			r.logInfo(redisCluster.NamespacedName(), "Successfully created statefulset")

			// Set the config checksum annotation
			statefulSet = r.addConfigChecksumAnnotation(statefulSet, redisCluster)

			ctrl.SetControllerReference(redisCluster, statefulSet, r.Scheme)
			createSsetError = r.Client.Create(ctx, statefulSet)
			if createSsetError != nil {
				if errors.IsAlreadyExists(createSsetError) {
					r.logInfo(redisCluster.NamespacedName(), "StatefulSet already exists")
				} else {
					r.logError(redisCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				}
			}
		} else {
			r.logError(redisCluster.NamespacedName(), err, "Getting statefulset data failed", "statefulset", statefulSet.Name)
			return immediateRequeue, err
		}
	} else {
		// Check StatefulSet <-> RedisCluster coherence

		// When scaling up before upgrading we can have inconsistencies currReadyNodes <> currSsetReplicas
		// we skip these checks till the sacling up is done.
		if redisCluster.Status.Status == redisv1.StatusUpgrading && redisCluster.Status.Substatus.Status == redisv1.SubstatusUpgradingScalingUp {
			return immediateRequeue, nil
		}

		currSsetReplicas := *(statefulSet.Spec.Replicas)
		realExpectedReplicas := int32(redisCluster.NodesNeeded())
		immediateRequeue = false
		var err error
		if redisCluster.Status.Status == "" || redisCluster.Status.Status == redisv1.StatusInitializing {
			if currSsetReplicas != realExpectedReplicas {
				immediateRequeue = true
				r.logInfo(redisCluster.NamespacedName(), "Replicas updated before reaching Configuring status: aligning StatefulSet <-> RedisCluster replicas",
					"StatefulSet replicas", currSsetReplicas, "RedisCluster replicas", realExpectedReplicas)
				statefulSet.Spec.Replicas = &realExpectedReplicas
				_, err = r.updateStatefulSet(ctx, statefulSet, redisCluster)
				if err != nil {
					r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
				}
				return immediateRequeue, err
			}
		}
		// if realExpectedReplicas < currSsetReplicas {
		// 	// Inconsistency: if a scaleup could not be completed because of a lack of resources that prevented
		// 	// all the needed pods from being created
		// 	// StatefulSet replicas are then aligned with RedisCluster replicas
		// 	r.logInfo(redisCluster.NamespacedName(), "Not all required pods instantiated: aligning StatefulSet <-> RedisCluster replicas",
		// 		"StatefulSet replicas", currSsetReplicas, "RedisCluster replicas", realExpectedReplicas)
		// 	statefulSet.Spec.Replicas = &realExpectedReplicas
		// 	_, err = r.updateStatefulSet(ctx, statefulSet, redisCluster)
		// 	if err != nil {
		// 		r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
		// 	}
		// }
		return immediateRequeue, err
	}
	return immediateRequeue, nil
}

func (r *RedisClusterReconciler) createStatefulSet(ctx context.Context, req ctrl.Request, spec redisv1.RedKeyClusterSpec, labels map[string]string, configmap *corev1.ConfigMap) (*v1.StatefulSet, error) {
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

func (r *RedisClusterReconciler) inferResources(req ctrl.Request, spec redisv1.RedKeyClusterSpec, configmap *corev1.ConfigMap, statefulSet *v1.StatefulSet) (*v1.StatefulSet, error) {
	config := spec.Config
	desiredConfig := redis.MergeWithDefaultConfig(
		redis.ConfigStringToMap(config),
		spec.Ephemeral,
		spec.ReplicasPerMaster)

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

func (r *RedisClusterReconciler) overrideStatefulSet(req ctrl.Request, redisCluster *redisv1.RedKeyCluster, statefulSet *v1.StatefulSet) (*v1.StatefulSet, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redisCluster.Spec.Override == nil {
		redisCluster.Spec.Override = &redisv1.RedKeyClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redisCluster.Spec.Override.StatefulSet == nil {
		redisCluster.Spec.Override.StatefulSet = &v1.StatefulSet{}
	}

	// Apply the override
	patchedStatefulSet, err := redis.ApplyStsOverride(statefulSet, redisCluster.Spec.Override.StatefulSet)
	if err != nil {
		ctrl.Log.Error(err, "Error applying StatefulSet override")
		return statefulSet, false
	}

	// Apply the resources override if provided in containers without resources after patch
	if redisCluster.Spec.Resources != nil {
		for k := range patchedStatefulSet.Spec.Template.Spec.Containers {
			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits = redisCluster.Spec.Resources.Limits
			}

			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests = redisCluster.Spec.Resources.Requests
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

func (r *RedisClusterReconciler) checkAndCreateService(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedKeyCluster) (bool, error) {
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.logError(redisCluster.NamespacedName(), err, "Getting svc data failed")
			return false, err
		}

		// Create service otherwise
		r.logInfo(redisCluster.NamespacedName(), "Creating service")
		service := r.createService(req, redisCluster.Spec, redisCluster.GetObjectMeta().GetLabels())
		ctrl.SetControllerReference(redisCluster, service, r.Scheme)

		createSVCError := r.Client.Create(ctx, service)
		if createSVCError != nil {
			if !errors.IsAlreadyExists(createSVCError) {
				return false, createSVCError
			}

			r.logInfo(redisCluster.NamespacedName(), "Svc already exists")
			return false, nil
		}

		r.logInfo(redisCluster.NamespacedName(), "Successfully created service", "service", service.Name)
	} else {
		// Handle changes in spec.override.service
		patchedService, changed := r.overrideService(req, redisCluster, service)

		// Update service if changed
		if changed {
			service = patchedService
			err := r.Client.Update(ctx, service)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error updating service")
				return false, err
			}
			r.logInfo(redisCluster.NamespacedName(), "Successfully updated service", "service", service.Name)
		}
	}
	return false, nil
}

func (r *RedisClusterReconciler) createService(req ctrl.Request, spec redisv1.RedKeyClusterSpec, labels map[string]string) *corev1.Service {
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

func (r *RedisClusterReconciler) overrideService(req ctrl.Request, redisCluster *redisv1.RedKeyCluster, service *corev1.Service) (*corev1.Service, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redisCluster.Spec.Override == nil {
		redisCluster.Spec.Override = &redisv1.RedKeyClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redisCluster.Spec.Override.Service == nil {
		redisCluster.Spec.Override.Service = &corev1.Service{}
	}

	// Apply the override
	patchedService, err := redis.ApplyServiceOverride(service, redisCluster.Spec.Override.Service)
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

func (r *RedisClusterReconciler) allPodsReady(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, error) {
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.getStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}
	podsReady, err := kubernetes.AllPodsReady(ctx, r.Client, &listOptions, redisCluster.NodesNeeded())
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
		return false, err
	}
	return podsReady, nil
}

func (r *RedisClusterReconciler) getPersistentVolumeClaim(ctx context.Context, client client.Client, redisCluster *redisv1.RedKeyCluster, name string) (*corev1.PersistentVolumeClaim, error) {
	r.logInfo(redisCluster.NamespacedName(), "Getting persistent volume claim to be deleted ")
	pvc := &corev1.PersistentVolumeClaim{}
	err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: redisCluster.Namespace}, pvc)

	if err != nil {
		return nil, err
	}
	return pvc, nil
}

func (ef *RedisClusterReconciler) deletePVC(ctx context.Context, client client.Client, pvc *corev1.PersistentVolumeClaim) error {
	err := client.Delete(ctx, pvc)
	return err
}

// GetStatefulSetSelectorLabel returns the label key that should be used to find RedisCluster nodes for
// backwards compatibility with the old version RedisCluster.
// The implementation has been moved, and this exists merely as an alias for backward compatibility
func (r *RedisClusterReconciler) getStatefulSetSelectorLabel(rdcl *redisv1.RedKeyCluster) string {
	return kubernetes.GetStatefulSetSelectorLabel(context.TODO(), r.Client, rdcl)
}

func (r *RedisClusterReconciler) updateStatefulSet(ctx context.Context, statefulSet *v1.StatefulSet, redisCluster *redisv1.RedKeyCluster) (*v1.StatefulSet, error) {
	refreshedStatefulSet := &v1.StatefulSet{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: statefulSet.Namespace, Name: statefulSet.Name}, refreshedStatefulSet)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
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

func (r *RedisClusterReconciler) updateDeployment(ctx context.Context, deployment *v1.Deployment, redisCluster *redisv1.RedKeyCluster) (*v1.Deployment, error) {
	refreshedDeployment := &v1.Deployment{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, refreshedDeployment)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
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

func (r *RedisClusterReconciler) updateRdclReplicas(ctx context.Context, redisCluster *redisv1.RedKeyCluster, replicas int32) (*redisv1.RedKeyCluster, error) {
	refreshedRdcl := &redisv1.RedKeyCluster{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, refreshedRdcl)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		refreshedRdcl.Spec.Replicas = replicas
		var updateErr = r.Client.Update(ctx, refreshedRdcl)
		return updateErr
	})
	if err != nil {
		return nil, err
	}
	return refreshedRdcl, nil
}
