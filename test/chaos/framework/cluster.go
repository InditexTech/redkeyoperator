// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

// NOTE: This file is adapted from test/e2e/framework/redisclient.go for chaos tests.
// It contains the Redis cluster creation and management functions.
package framework

import (
	"context"
	"fmt"
	"os"
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	defaultConfig = `save ""
appendonly no
maxmemory 70mb`
	defaultTimeout    = 60 * time.Minute
	defaultRedisImage = "redis:8.4.0"
	defaultRobinImage = "localhost:5001/redkey-robin:dev"
	version           = "6.0.2"
)

// GetRedisImage returns the redis image from environment or default.
func GetRedisImage() string {
	if img := os.Getenv("REDIS_IMAGE"); img != "" {
		return img
	}
	return defaultRedisImage
}

// GetRobinImage returns the robin image from environment or default.
func GetRobinImage() string {
	if img := os.Getenv("ROBIN_IMAGE"); img != "" {
		return img
	}
	return defaultRobinImage
}

// CreateRedkeyCluster creates a RedkeyCluster CR using dynamic client.
func CreateRedkeyCluster(ctx context.Context, dc dynamic.Interface, namespace, name string, primaries int32) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	rc := buildRedkeyCluster(key, primaries, 0, "", GetRedisImage(), true, true, redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})
	return EnsureRedkeyCluster(ctx, dc, rc)
}

// GetStatefulSetReplicas returns the current replica count for a cluster's StatefulSet.
func GetStatefulSetReplicas(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) (int32, error) {
	sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	if sts.Spec.Replicas == nil {
		return 0, nil
	}
	return *sts.Spec.Replicas, nil
}

// buildRedkeyCluster constructs a RedkeyCluster object with the given parameters.
func buildRedkeyCluster(
	key types.NamespacedName,
	primaries, replicasPerPrimary int32,
	storage, image string,
	purgeKeys, ephemeral bool,
	pdb redkeyv1.Pdb,
	userOverride redkeyv1.RedkeyClusterOverrideSpec,
) *redkeyv1.RedkeyCluster {
	rc := &redkeyv1.RedkeyCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{"team": "team-a"},
		},
		Spec: redkeyv1.RedkeyClusterSpec{
			Auth:                 redkeyv1.RedisAuth{},
			Version:              version,
			Primaries:            primaries,
			Ephemeral:            ephemeral,
			Image:                image,
			Config:               defaultConfig,
			Resources:            buildResources(),
			PurgeKeysOnRebalance: &purgeKeys,
		},
	}

	if storage != "" {
		rc.Spec.DeletePVC = ptr.To(true)
		rc.Spec.Ephemeral = false
		rc.Spec.Storage = storage
		rc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	if pdb != (redkeyv1.Pdb{}) {
		rc.Spec.Pdb = pdb
	}

	if replicasPerPrimary > 0 {
		rc.Spec.ReplicasPerPrimary = replicasPerPrimary
	}

	// Build override with security context
	var ov redkeyv1.RedkeyClusterOverrideSpec
	if userOverride.StatefulSet != nil || userOverride.Service != nil {
		ov = userOverride
	}

	if ov.StatefulSet == nil {
		ov.StatefulSet = &redkeyv1.PartialStatefulSet{
			Spec: &redkeyv1.PartialStatefulSetSpec{
				Template: &redkeyv1.PartialPodTemplateSpec{},
			},
		}
	}
	if ov.StatefulSet.Spec == nil {
		ov.StatefulSet.Spec = &redkeyv1.PartialStatefulSetSpec{
			Template: &redkeyv1.PartialPodTemplateSpec{},
		}
	}
	if ov.StatefulSet.Spec.Template == nil {
		ov.StatefulSet.Spec.Template = &redkeyv1.PartialPodTemplateSpec{}
	}
	podSpec := &ov.StatefulSet.Spec.Template.Spec
	if podSpec.SecurityContext == nil {
		podSpec.SecurityContext = &corev1.PodSecurityContext{}
	}
	podSpec.SecurityContext.RunAsNonRoot = ptr.To(true)
	podSpec.SecurityContext.RunAsUser = ptr.To(int64(1001))
	podSpec.SecurityContext.RunAsGroup = ptr.To(int64(1001))
	podSpec.SecurityContext.FSGroup = ptr.To(int64(1001))

	rc.Spec.Override = &ov

	// Robin configuration
	robinImage := GetRobinImage()
	rc.Spec.Robin = &redkeyv1.RobinSpec{
		Template: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:            "robin",
						Image:           robinImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8080,
								Name:          "http",
								Protocol:      corev1.ProtocolTCP,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      key.Name + "-robin-config",
								MountPath: "/opt/conf/configmap",
							},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To(int64(1001)),
							AllowPrivilegeEscalation: ptr.To(false),
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: key.Name + "-robin-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: key.Name + "-robin",
								},
								DefaultMode: ptr.To(int32(420)),
							},
						},
					},
				},
			},
		},
		Config: &redkeyv1.RobinConfig{
			Reconciler: &redkeyv1.RobinConfigReconciler{
				IntervalSeconds:                 ptr.To(30),
				OperationCleanUpIntervalSeconds: ptr.To(30),
			},
			Cluster: &redkeyv1.RobinConfigCluster{
				HealthProbePeriodSeconds: ptr.To(60),
				HealingTimeSeconds:       ptr.To(60),
				MaxRetries:               ptr.To(10),
				BackOff:                  ptr.To(10),
			},
		},
	}

	return rc
}

// WaitForReady waits until the cluster status is Ready using dynamic client.
func WaitForReady(ctx context.Context, dc dynamic.Interface, key types.NamespacedName, timeout time.Duration) (*redkeyv1.RedkeyCluster, error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	var last string
	if err := wait.PollUntilContextTimeout(
		ctx, 3*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			rc, err := GetRedkeyCluster(ctx, dc, key.Namespace, key.Name)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, nil
			}
			last = rc.Status.Status
			return last == redkeyv1.StatusReady, nil
		},
	); err != nil {
		return nil, fmt.Errorf(
			"timed out after %s waiting for Ready (last seen %q): %w",
			timeout, last, err,
		)
	}

	return GetRedkeyCluster(ctx, dc, key.Namespace, key.Name)
}

func buildResources() *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}
