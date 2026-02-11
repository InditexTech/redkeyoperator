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
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	redis "github.com/inditextech/redkeyoperator/internal/redis"
	"github.com/inditextech/redkeyoperator/internal/robin"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *RedkeyClusterReconciler) checkAndCreateRobin(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) (bool, error) {
	// Populate robin spec if not provided. This to handle the case where the user removes the robin spec of an existing cluster. The robin objects will be deleted.
	if redkeyCluster.Spec.Robin == nil {
		redkeyCluster.Spec.Robin = &redkeyv1.RobinSpec{
			Config:   nil,
			Template: nil,
		}
	}

	// Robin configmap
	if err := r.handleRobinConfig(ctx, req, redkeyCluster); err != nil {
		return false, err
	}

	// Robin deployment
	immediateRequeue := r.handleRobinDeployment(ctx, req, redkeyCluster)

	return immediateRequeue, nil
}

func (r *RedkeyClusterReconciler) handleRobinConfig(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) error {
	// Get robin configmap
	existingConfigMap, err := r.FindExistingConfigMapFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-robin", Namespace: redkeyCluster.Namespace}})

	// Robin configmap not provided: delete configmap if exists
	if redkeyCluster.Spec.Robin.Config == nil {
		r.deleteRobinObject(ctx, existingConfigMap, redkeyCluster, "configmap")
		return nil
	}

	// Robin configmap not found: create configmap
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.logError(redkeyCluster.NamespacedName(), err, "Getting robin configmap failed")
			return nil
		}

		// Create Robin ConfigMap
		newConfigMap := r.createRobinConfigMap(req, redkeyCluster.Spec, redkeyCluster.GetLabels())
		r.createRobinObject(ctx, newConfigMap, redkeyCluster, "configmap")
		return nil
	}

	// Robin configmap found: check if it needs to be updated
	var existingConfig, declaredConfig robin.Configuration
	if err := yaml.Unmarshal([]byte(existingConfigMap.Data["application-configmap.yml"]), &existingConfig); err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error parsing existing Robin configuration")
		return err
	}

	declaredConfig = r.getRobinConfiguration(req, redkeyCluster.Spec)

	if robin.CompareConfigurations(&declaredConfig, &existingConfig) {
		return nil // No changes detected, no more to do here
	}

	// Robin config changed: update configmap
	r.logInfo(req.NamespacedName, "Robin's configuration has changed, updating ConfigMap")

	// Serialize robin configuration to YAML
	configYAML, err := yaml.Marshal(declaredConfig)
	if err != nil {
		r.logError(req.NamespacedName, err, "Error generating robin configuration YAML")
		return err
	}
	existingConfigMap.Data["application-configmap.yml"] = string(configYAML)
	r.updateRobinObject(ctx, existingConfigMap, redkeyCluster, "configmap")

	// Add checksum to the deployment annotations to force the deployment rollout
	if redkeyCluster.Spec.Robin.Template != nil {
		if redkeyCluster.Spec.Robin.Template.Metadata.Annotations == nil {
			redkeyCluster.Spec.Robin.Template.Metadata.Annotations = make(map[string]string)
		}
		redkeyCluster.Spec.Robin.Template.Metadata.Annotations["checksum/config"] = fmt.Sprintf("%x", md5.Sum([]byte(string(configYAML))))
	}

	return nil
}

func (r *RedkeyClusterReconciler) handleRobinDeployment(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) (immediateRequeue bool) {
	// Get robin deployment
	deployment, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-robin", Namespace: redkeyCluster.Namespace}})

	// Robin deployment template not provided: delete deployment if exists
	if redkeyCluster.Spec.Robin.Template == nil {
		r.deleteRobinObject(ctx, deployment, redkeyCluster, "deployment")
		return false
	}

	// Robin deployment not found: create deployment
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.logError(redkeyCluster.NamespacedName(), err, "Getting robin deployment failed")
			return false
		}

		// Create Robin Deployment
		robinDeployment := r.createRobinDeployment(req, redkeyCluster, redkeyCluster.GetLabels())
		r.createRobinObject(ctx, robinDeployment, redkeyCluster, "deployment")
		return false
	}

	// Robin deployment found: check if it needs to be updated
	patchedPodTemplateSpec, changed := r.overrideRobinDeployment(req, redkeyCluster, deployment.Spec.Template)

	// Check if Robin deployment needs to be scaled up (e.g., after scaling from 0 primaries)
	needsScaleUp := deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 && redkeyCluster.Spec.Primaries > 0
	if needsScaleUp {
		r.logInfo(redkeyCluster.NamespacedName(), "Scaling up Robin deployment from 0 replicas")
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		changed = true

		// Update replicas in robin configuration
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Primaries), int(redkeyCluster.Spec.ReplicasPerPrimary))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin primaries/replicasPerPrimary")
		}
	}

	if !changed {
		return needsScaleUp
	}

	// Robin deployment changed: update deployment
	deployment.Spec.Template = patchedPodTemplateSpec
	r.updateRobinObject(ctx, deployment, redkeyCluster, "deployment")

	return needsScaleUp
}

func (r *RedkeyClusterReconciler) createRobinObject(ctx context.Context, obj client.Object, redkeyCluster *redkeyv1.RedkeyCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	ctrl.SetControllerReference(redkeyCluster, obj, r.Scheme)

	r.logInfo(redkeyCluster.NamespacedName(), "Creating robin "+kind)
	err := r.Client.Create(ctx, obj)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			r.logError(redkeyCluster.NamespacedName(), err, "Error creating robin "+kind)
			return err
		}
		r.logInfo(redkeyCluster.NamespacedName(), "robin "+kind+" already exists")
		return nil
	}

	r.logInfo(redkeyCluster.NamespacedName(), "Successfully created robin "+kind, kind, redkeyCluster.Name+"-robin")
	return nil
}

func (r *RedkeyClusterReconciler) updateRobinObject(ctx context.Context, obj client.Object, redkeyCluster *redkeyv1.RedkeyCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.logInfo(redkeyCluster.NamespacedName(), "Updating robin "+kind)
	err := r.Client.Update(ctx, obj)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating robin "+kind)
		return err
	}

	r.logInfo(redkeyCluster.NamespacedName(), "Successfully updated robin "+kind, kind, redkeyCluster.Name+"-robin")
	return nil
}

func (r *RedkeyClusterReconciler) deleteRobinObject(ctx context.Context, obj client.Object, redkeyCluster *redkeyv1.RedkeyCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.logInfo(redkeyCluster.NamespacedName(), "Deleting robin "+kind)
	err := r.Client.Delete(ctx, obj)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error deleting robin "+kind)
		return err
	}

	r.logInfo(redkeyCluster.NamespacedName(), "Successfully deleted robin "+kind, kind, redkeyCluster.Name+"-robin")
	return nil
}

func (r *RedkeyClusterReconciler) overrideRobinDeployment(req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, podTemplateSpec corev1.PodTemplateSpec) (corev1.PodTemplateSpec, bool) {
	// Convert PartialPodTemplateSpec to PodTemplateSpec
	partialTemplate := r.convertPartialPodTemplateToFull(redkeyCluster.Spec.Robin.Template)

	// Apply the override
	patchedPodTemplateSpec, err := redis.ApplyPodTemplateSpecOverride(podTemplateSpec, partialTemplate)
	if err != nil {
		ctrl.Log.Error(err, "Error applying pod template spec override")
		return podTemplateSpec, false
	}

	// Check if the override changes something in the original PodTemplateSpec
	changed := !reflect.DeepEqual(podTemplateSpec.Labels, patchedPodTemplateSpec.Labels) || !reflect.DeepEqual(podTemplateSpec.Annotations, patchedPodTemplateSpec.Annotations) || !reflect.DeepEqual(podTemplateSpec.Spec, patchedPodTemplateSpec.Spec)

	if changed {
		r.logInfo(req.NamespacedName, "Detected robin deployment change")
	}

	return *patchedPodTemplateSpec, changed
}

func (r *RedkeyClusterReconciler) createRobinDeployment(req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, labels map[string]string) *v1.Deployment {
	var replicas = int32(1)
	// Create deployment with 0 replicas if the cluster has 0 primaries
	// to avoid pod errors due to missing redis nodes.
	if redkeyCluster.Spec.Primaries == 0 {
		replicas = 0
	}
	d := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Template: r.convertPartialPodTemplateToFull(redkeyCluster.Spec.Robin.Template),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{redis.RedkeyClusterLabel: req.Name, r.getStatefulSetSelectorLabel(redkeyCluster): "robin"},
			},
			Replicas: &replicas,
		},
	}
	d.Labels[redis.RedkeyClusterLabel] = req.Name
	d.Labels[redis.RedkeyClusterComponentLabel] = "robin"
	d.Spec.Template.Labels = make(map[string]string)
	maps.Copy(d.Spec.Template.Labels, labels)
	d.Spec.Template.Labels[redis.RedkeyClusterLabel] = req.Name
	d.Spec.Template.Labels[redis.RedkeyClusterComponentLabel] = "robin"
	if redkeyCluster.Spec.Robin.Template.Metadata.Labels != nil {
		maps.Copy(d.Spec.Template.Labels, redkeyCluster.Spec.Robin.Template.Metadata.Labels)
	}

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

func (r *RedkeyClusterReconciler) convertPartialPodTemplateToFull(partial *redkeyv1.PartialPodTemplateSpec) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: partial.Metadata,
		Spec: corev1.PodSpec{
			Containers:                    partial.Spec.Containers,
			InitContainers:                partial.Spec.InitContainers,
			EphemeralContainers:           partial.Spec.EphemeralContainers,
			RestartPolicy:                 partial.Spec.RestartPolicy,
			TerminationGracePeriodSeconds: partial.Spec.TerminationGracePeriodSeconds,
			DNSPolicy:                     partial.Spec.DNSPolicy,
			NodeSelector:                  partial.Spec.NodeSelector,
			ServiceAccountName:            partial.Spec.ServiceAccountName,
			NodeName:                      partial.Spec.NodeName,
			HostNetwork:                   partial.Spec.HostNetwork,
			HostPID:                       partial.Spec.HostPID,
			HostIPC:                       partial.Spec.HostIPC,
			ShareProcessNamespace:         partial.Spec.ShareProcessNamespace,
			SecurityContext:               partial.Spec.SecurityContext,
			ImagePullSecrets:              partial.Spec.ImagePullSecrets,
			Hostname:                      partial.Spec.Hostname,
			Subdomain:                     partial.Spec.Subdomain,
			Affinity:                      partial.Spec.Affinity,
			SchedulerName:                 partial.Spec.SchedulerName,
			Tolerations:                   partial.Spec.Tolerations,
			HostAliases:                   partial.Spec.HostAliases,
			PriorityClassName:             partial.Spec.PriorityClassName,
			Priority:                      partial.Spec.Priority,
			DNSConfig:                     partial.Spec.DNSConfig,
			ReadinessGates:                partial.Spec.ReadinessGates,
			RuntimeClassName:              partial.Spec.RuntimeClassName,
			EnableServiceLinks:            partial.Spec.EnableServiceLinks,
			PreemptionPolicy:              partial.Spec.PreemptionPolicy,
			Overhead:                      partial.Spec.Overhead,
			TopologySpreadConstraints:     partial.Spec.TopologySpreadConstraints,
			Volumes:                       partial.Spec.Volumes,
			ActiveDeadlineSeconds:         partial.Spec.ActiveDeadlineSeconds,
			AutomountServiceAccountToken:  partial.Spec.AutomountServiceAccountToken,
		},
	}
}

func (r *RedkeyClusterReconciler) getRobinConfiguration(req ctrl.Request, spec redkeyv1.RedkeyClusterSpec) robin.Configuration {
	// Set default values for Robin configuration
	reconcilerInterval := 30
	reconcilerCleanupInterval := 30
	clusterHealthProbe := 60
	clusterHealingTime := 60
	clusterMaxRetries := 10
	clusterBackOff := 10
	metricsInterval := 60
	var metricsRedisInfoKeys []string

	// Override with provided values if they exist
	if spec.Robin != nil && spec.Robin.Config != nil {
		if spec.Robin.Config.Reconciler != nil {
			if spec.Robin.Config.Reconciler.IntervalSeconds != nil {
				reconcilerInterval = *spec.Robin.Config.Reconciler.IntervalSeconds
			}
			if spec.Robin.Config.Reconciler.OperationCleanUpIntervalSeconds != nil {
				reconcilerCleanupInterval = *spec.Robin.Config.Reconciler.OperationCleanUpIntervalSeconds
			}
		}
		if spec.Robin.Config.Cluster != nil {
			if spec.Robin.Config.Cluster.HealthProbePeriodSeconds != nil {
				clusterHealthProbe = *spec.Robin.Config.Cluster.HealthProbePeriodSeconds
			}
			if spec.Robin.Config.Cluster.HealingTimeSeconds != nil {
				clusterHealingTime = *spec.Robin.Config.Cluster.HealingTimeSeconds
			}
			if spec.Robin.Config.Cluster.MaxRetries != nil {
				clusterMaxRetries = *spec.Robin.Config.Cluster.MaxRetries
			}
			if spec.Robin.Config.Cluster.BackOff != nil {
				clusterBackOff = *spec.Robin.Config.Cluster.BackOff
			}
		}
		if spec.Robin.Config.Metrics != nil {
			if spec.Robin.Config.Metrics.IntervalSeconds != nil {
				metricsInterval = *spec.Robin.Config.Metrics.IntervalSeconds
			}
			metricsRedisInfoKeys = spec.Robin.Config.Metrics.RedisInfoKeys
		}
	}

	config := robin.Configuration{
		Metadata: map[string]string{
			"namespace": req.Namespace,
		},
		Redis: robin.RedisConfig{
			Standalone: false,
			Reconciler: robin.RedisReconcilerConfig{
				IntervalSeconds:                 reconcilerInterval,
				OperationCleanupIntervalSeconds: reconcilerCleanupInterval,
			},
			Cluster: robin.RedkeyClusterConfig{
				Namespace:                req.Namespace,
				Name:                     req.Name,
				Primaries:                int(spec.Primaries),
				ReplicasPerPrimary:       int(spec.ReplicasPerPrimary),
				Status:                   redkeyv1.RobinStatusUnknown,
				Ephemeral:                spec.Ephemeral,
				HealthProbePeriodSeconds: clusterHealthProbe,
				HealingTimeSeconds:       clusterHealingTime,
				MaxRetries:               clusterMaxRetries,
				BackOff:                  time.Duration(clusterBackOff) * time.Second,
			},
			Metrics: robin.RedisMetricsConfig{
				IntervalSeconds: metricsInterval,
				RedisInfoKeys:   metricsRedisInfoKeys,
			},
		},
	}

	return config
}

func (r *RedkeyClusterReconciler) createRobinConfigMap(req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, labels map[string]string) *corev1.ConfigMap {

	// Get robin configuration from RedkeyCluster spec
	config := r.getRobinConfiguration(req, spec)

	// Serialize robin configuration to YAML
	configYAML, err := yaml.Marshal(config)
	if err != nil {
		r.logError(req.NamespacedName, err, "Error generating robin configuration YAML")
		return nil
	}

	// Create configmap object
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"application-configmap.yml": string(configYAML)},
	}

	r.logInfo(req.NamespacedName, "Robin ConfigMap created")
	return &cm
}

func (r *RedkeyClusterReconciler) scaleDownRobin(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) {
	if redkeyCluster.Spec.Robin != nil {
		if redkeyCluster.Spec.Robin.Template != nil {
			mdep, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-robin", Namespace: redkeyCluster.Namespace}})
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Cannot find existing robin deployment", "deployment", redkeyCluster.Name+"-robin")
			} else {
				// Scaledown
				*mdep.Spec.Replicas = 0
				mdep, err = r.updateDeployment(ctx, mdep, redkeyCluster)
				if err != nil {
					r.logError(redkeyCluster.NamespacedName(), err, "Failed to update Deployment replicas")
				} else {
					r.logInfo(redkeyCluster.NamespacedName(), "Robin Deployment updated", "Replicas", mdep.Spec.Replicas)
				}
			}
		}
	}
}
