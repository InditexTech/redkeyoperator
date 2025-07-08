// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/md5"
	"fmt"
	"maps"
	"reflect"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	redis "github.com/inditextech/redisoperator/internal/redis"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *RedisClusterReconciler) checkAndCreateRobin(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Populate robin spec if not provided. This to handle the case where the user removes the robin spec of an existing cluster. The robin objects will be deleted.
	if redisCluster.Spec.Robin == nil {
		redisCluster.Spec.Robin = &redisv1.RobinSpec{
			Config:   nil,
			Template: nil,
		}
	}

	// Robin configmap
	r.handleRobinConfig(ctx, req, redisCluster)

	// Robin deployment
	r.handleRobinDeployment(ctx, req, redisCluster)
}

func (r *RedisClusterReconciler) handleRobinConfig(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Get robin configmap
	configmap, err := r.FindExistingConfigMapFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})

	// Robin configmap not provided: delete configmap if exists
	if redisCluster.Spec.Robin.Config == nil {
		r.deleteRobinObject(ctx, configmap, redisCluster, "configmap")
		return
	}

	// Robin configmap not found: create configmap
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Getting robin configmap failed")
			return
		}

		// Create Robin ConfigMap
		robinCM := r.createRobinConfigMap(req, redisCluster.Spec, *redisCluster.Spec.Labels)
		r.createRobinObject(ctx, robinCM, redisCluster, "configmap")
		return
	}

	// Robin configmap found: check if it needs to be updated
	if *redisCluster.Spec.Robin.Config == configmap.Data["application-configmap.yml"] {
		return
	}

	// Robin configmap changed: update configmap
	configmap.Data["application-configmap.yml"] = *redisCluster.Spec.Robin.Config
	r.updateRobinObject(ctx, configmap, redisCluster, "configmap")

	// Add checksum to the deployment annotations to force the deployment rollout
	if redisCluster.Spec.Robin.Template != nil {
		if redisCluster.Spec.Robin.Template.Annotations == nil {
			redisCluster.Spec.Robin.Template.Annotations = make(map[string]string)
		}
		redisCluster.Spec.Robin.Template.Annotations["checksum/config"] = fmt.Sprintf("%x", md5.Sum([]byte(*redisCluster.Spec.Robin.Config)))
	}
}

func (r *RedisClusterReconciler) handleRobinDeployment(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Get robin deployment
	deployment, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})

	// Robin deployment template not provided: delete deployment if exists
	if redisCluster.Spec.Robin.Template == nil {
		r.deleteRobinObject(ctx, deployment, redisCluster, "deployment")
		return
	}

	// Robin deployment not found: create deployment
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Getting robin deployment failed")
			return
		}

		// Create Robin Deployment
		robinDeployment := r.createRobinDeployment(req, redisCluster, *redisCluster.Spec.Labels)
		r.createRobinObject(ctx, robinDeployment, redisCluster, "deployment")
		return
	}

	// Robin deployment found: check if it needs to be updated
	patchedPodTemplateSpec, changed := r.overrideRobinDeployment(ctx, req, redisCluster, deployment.Spec.Template)
	if !changed {
		return
	}

	// Robin deployment changed: update deployment
	deployment.Spec.Template = patchedPodTemplateSpec
	r.updateRobinObject(ctx, deployment, redisCluster, "deployment")
}

func (r *RedisClusterReconciler) createRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	ctrl.SetControllerReference(redisCluster, obj, r.Scheme)

	r.LogInfo(redisCluster.NamespacedName(), "Creating robin "+kind)
	err := r.Client.Create(ctx, obj)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Error creating robin "+kind)
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "robin "+kind+" already exists")
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully created robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) updateRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Updating robin "+kind)
	err := r.Client.Update(ctx, obj)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error updating robin "+kind)
		return err
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully updated robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) deleteRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Deleting robin "+kind)
	err := r.Client.Delete(ctx, obj)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error deleting robin "+kind)
		return err
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully deleted robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) overrideRobinDeployment(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, podTemplateSpec corev1.PodTemplateSpec) (corev1.PodTemplateSpec, bool) {
	// Apply the override
	patchedPodTemplateSpec, err := redis.ApplyPodTemplateSpecOverride(podTemplateSpec, *redisCluster.Spec.Robin.Template)
	if err != nil {
		ctrl.Log.Error(err, "Error applying pod template spec override")
		return podTemplateSpec, false
	}

	// Check if the override changes something in the original PodTemplateSpec
	changed := !reflect.DeepEqual(podTemplateSpec.Labels, patchedPodTemplateSpec.Labels) || !reflect.DeepEqual(podTemplateSpec.Annotations, patchedPodTemplateSpec.Annotations) || !reflect.DeepEqual(podTemplateSpec.Spec, patchedPodTemplateSpec.Spec)

	if changed {
		r.LogInfo(req.NamespacedName, "Detected robin deployment change")
	} else {
		r.LogInfo(req.NamespacedName, "No robin deployment change detected")
	}

	return *patchedPodTemplateSpec, changed
}

func (r *RedisClusterReconciler) createRobinDeployment(req ctrl.Request, rediscluster *redisv1.RedisCluster, labels map[string]string) *v1.Deployment {
	var replicas = int32(1)
	d := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Template: *rediscluster.Spec.Robin.Template,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{redis.RedisClusterLabel: req.Name, r.GetStatefulSetSelectorLabel(rediscluster): "robin"},
			},
			Replicas: &replicas,
		},
	}
	d.Labels[redis.RedisClusterLabel] = req.Name
	d.Labels[redis.RedisClusterComponentLabel] = "robin"
	d.Spec.Template.Labels = make(map[string]string)
	maps.Copy(d.Spec.Template.Labels, labels)
	d.Spec.Template.Labels[redis.RedisClusterLabel] = req.Name
	d.Spec.Template.Labels[redis.RedisClusterComponentLabel] = "robin"
	maps.Copy(d.Spec.Template.Labels, rediscluster.Spec.Robin.Template.Labels)

	for i, container := range d.Spec.Template.Spec.Containers {
		if container.Resources.Requests == nil {
			d.Spec.Template.Spec.Containers[i].Resources.Requests = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("20m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
		if container.Resources.Limits == nil {
			d.Spec.Template.Spec.Containers[i].Resources.Limits = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
	}

	return d
}

func (r *RedisClusterReconciler) createRobinConfigMap(req ctrl.Request, spec redisv1.RedisClusterSpec, labels map[string]string) *corev1.ConfigMap {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"application-configmap.yml": *spec.Robin.Config},
	}

	r.LogInfo(req.NamespacedName, "Generated robin configmap")
	return &cm
}

func (r *RedisClusterReconciler) scaleDownRobin(ctx context.Context, redisCluster *redisv1.RedisCluster) {
	if redisCluster.Spec.Robin != nil {
		if redisCluster.Spec.Robin.Template != nil {
			mdep, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Cannot find existing robin deployment", "deployment", redisCluster.Name+"-robin")
			} else {
				// Scaledown
				*mdep.Spec.Replicas = 0
				mdep, err = r.UpdateDeployment(ctx, mdep, redisCluster)
				if err != nil {
					r.LogError(redisCluster.NamespacedName(), err, "Failed to update Deployment replicas")
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Robin Deployment updated", "Replicas", mdep.Spec.Replicas)
				}
			}
		}
	}
}
