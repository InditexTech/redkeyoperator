// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package redis

import (
	"reflect"
	"strings"
	"testing"
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/common"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var replicas = int32(3)
var defaultLabels = map[string]string{
	RedkeyClusterLabel:          "redkeycluster",
	RedkeyClusterComponentLabel: common.ComponentLabelRedis,
}

var redisStatefulSet = &appsv1.StatefulSet{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "redkeycluster",
		Namespace: "default",
		Labels:    defaultLabels,
		CreationTimestamp: metav1.Time{
			Time: time.Unix(1, 0),
		},
	},
	Spec: appsv1.StatefulSetSpec{
		Replicas:            &replicas,
		PodManagementPolicy: appsv1.ParallelPodManagement,
		Selector: &metav1.LabelSelector{
			MatchLabels: defaultLabels,
		},
		ServiceName: "redkeycluster",
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: defaultLabels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "redis",
						Image: "redis:6.0.15",
						Ports: []corev1.ContainerPort{
							{
								Name:          "client",
								ContainerPort: RedisCommPort,
							},
							{
								Name:          "gossip",
								ContainerPort: RedisGossPort,
							},
						},
						Command:        []string{"redis-server", "/conf/redis.conf"},
						LivenessProbe:  CreateProbe(15, 5),
						ReadinessProbe: CreateProbe(10, 5),
						Resources: corev1.ResourceRequirements{
							Limits:   corev1.ResourceList{},
							Requests: corev1.ResourceList{},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "config",
								MountPath: "/conf",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
								Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
							},
						},
					},
				},
			},
		},
	},
	Status: appsv1.StatefulSetStatus{
		Replicas:      3,
		ReadyReplicas: 3,
	},
}

var redisService = &corev1.Service{
	TypeMeta: metav1.TypeMeta{
		Kind: "Service", APIVersion: "v1",
	},
	ObjectMeta: metav1.ObjectMeta{
		Name:      "redkeycluster",
		Namespace: "default",
		Labels:    defaultLabels,
	},
	Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{
			{
				Name:     "client",
				Protocol: "TCP",
				Port:     RedisCommPort,
				TargetPort: intstr.IntOrString{Type: 0,
					IntVal: RedisCommPort,
				},
			},
		},
		Selector:  map[string]string{RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis},
		ClusterIP: "None",
	},
}

var podTemplateSpec = &corev1.PodTemplateSpec{
	ObjectMeta: metav1.ObjectMeta{
		Labels: defaultLabels,
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "redis",
				Image: "redis:6.0.15",
				Ports: []corev1.ContainerPort{
					{
						Name:          "client",
						ContainerPort: RedisCommPort,
					},
					{
						Name:          "gossip",
						ContainerPort: RedisGossPort,
					},
				},
				Command:        []string{"redis-server", "/conf/redis.conf"},
				LivenessProbe:  CreateProbe(15, 5),
				ReadinessProbe: CreateProbe(10, 5),
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("100Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("20m"),
						corev1.ResourceMemory: resource.MustParse("100Mi"),
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "config",
						MountPath: "/conf",
					},
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
						Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
					},
				},
			},
		},
	},
}

func TestConfigStringToMap(t *testing.T) {
	type args struct {
		config string
	}
	tests := []struct {
		name string
		args args
		want map[string][]string
	}{
		{
			"single-entry", args{`maxmemory 500mb`},
			map[string][]string{"maxmemory": {"500mb"}},
		},
		{
			"whitespace-around",
			args{`

							maxmemory 500mb
							maxmemory-samples 5
							slaveof 127.0.0.1 6380

							`,
			},
			map[string][]string{"maxmemory": {"500mb"}, "maxmemory-samples": {"5"}, "slaveof": {"127.0.0.1 6380"}},
		},
		{
			"whitespace-between",
			args{`maxmemory    500mb
							maxmemory-samples 5`,
			},
			map[string][]string{"maxmemory": {"500mb"}, "maxmemory-samples": {"5"}},
		},
		{
			"loadmodule",
			args{`
							loadmodule /opt/redis-stack/lib/redisearch.so
							maxmemory-samples 5`,
			},
			map[string][]string{"loadmodule": {"/opt/redis-stack/lib/redisearch.so"}, "maxmemory-samples": {"5"}},
		},
		{
			"multiloadmodule",
			args{`
							loadmodule /opt/redis-stack/lib/redisearch.so
							loadmodule /opt/redis-stack/lib/rejson.so
							maxmemory-samples 5`,
			},
			map[string][]string{"loadmodule": {"/opt/redis-stack/lib/redisearch.so", "/opt/redis-stack/lib/rejson.so"}, "maxmemory-samples": {"5"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfigStringToMap(tt.args.config); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ConfigStringToMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeWithDefaultConfig(t *testing.T) {
	type args struct {
		custom    map[string][]string
		ephemeral bool
	}
	tests := []struct {
		name string
		args args
		want map[string][]string
	}{
		{
			"forbidden-override",
			args{map[string][]string{"maxmemory": {"2gb"}, "cluster-enabled": {"no"}}, false},
			map[string][]string{"maxmemory": {"2gb"}, "cluster-enabled": {"yes"}},
		},
		{
			"defaults-not-set",
			args{map[string][]string{}, false},
			map[string][]string{"maxmemory": {"1600mb"}, "cluster-enabled": {"yes"}},
		},
		{
			"ephemeral-mode-true",
			args{map[string][]string{"maxmemory": {"2gb"}, "cluster-enabled": {"no"}}, true},
			map[string][]string{"maxmemory": {"2gb"}, "cluster-enabled": {"yes"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeWithDefaultConfig(tt.args.custom, tt.args.ephemeral, int32(0))
			if tt.args.ephemeral && (got["dir"][0] != "/tmp" || got["cluster-config-file"][0] != "/tmp/nodes.conf") {
				t.Errorf("MergeWithDefaultConfig() configmap does not match ephemeral config")
			}
			if !tt.args.ephemeral && (got["dir"][0] != "/data" || got["cluster-config-file"][0] != "/data/nodes.conf") {
				t.Errorf("MergeWithDefaultConfig() configmap does not match non-ephemeral config")
			}
			for k, v := range tt.want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("MergeWithDefaultConfig() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestConvertRedisMemToMbytes(t *testing.T) {
	type args struct {
		maxMemory string
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantErr bool
	}{
		{"mb", args{maxMemory: "300mb"}, 300, false},
		{"m", args{maxMemory: "300m"}, 300, false},
		{"kb", args{maxMemory: "3000kb"}, 2, false},
		{"gb", args{maxMemory: "5gb"}, 5120, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertRedisMemToMbytes(tt.args.maxMemory)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertRedisMemToMbytes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ConvertRedisMemToMbytes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMapToConfigString(t *testing.T) {
	type args struct {
		config map[string][]string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"one",
			args{config: map[string][]string{"loadmodule": {"/opt/redis-stack/lib/redisearch.so"}}},
			"loadmodule /opt/redis-stack/lib/redisearch.so",
		},
		{
			"two",
			args{config: map[string][]string{"loadmodule": {"/opt/redis-stack/lib/redisearch.so"}, "maxmemory": {"500mb"}}},
			"loadmodule /opt/redis-stack/lib/redisearch.so\nmaxmemory 500mb",
		},
		{
			"three",
			args{config: map[string][]string{"loadmodule": {"/opt/redis-stack/lib/redisearch.so", "/opt/redis-stack/lib/rejson.so"}, "maxmemory": {"500mb"}}},
			"loadmodule /opt/redis-stack/lib/redisearch.so\nloadmodule /opt/redis-stack/lib/rejson.so\nmaxmemory 500mb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapToConfigString(tt.args.config); strings.TrimSpace(got) != tt.want {
				t.Errorf("MapToConfigString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_ApplyStsOverride(t *testing.T) {
	tests := []struct {
		name        string
		original    *appsv1.StatefulSet
		patch       *redkeyv1.PartialStatefulSet
		expected    *appsv1.StatefulSet
		expectedErr error
	}{
		{
			name:        "Patch empty",
			original:    redisStatefulSet,
			patch:       &redkeyv1.PartialStatefulSet{},
			expected:    redisStatefulSet,
			expectedErr: nil,
		},
		{
			name:     "Sidecar container",
			original: redisStatefulSet,
			patch: &redkeyv1.PartialStatefulSet{
				Spec: &redkeyv1.PartialStatefulSetSpec{
					Template: &redkeyv1.PartialPodTemplateSpec{
						Spec: redkeyv1.PartialPodSpec{
							Containers: []corev1.Container{
								{
									Name:  "sidecar",
									Image: "sidecar:1.0.0",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											"cpu":    resource.MustParse("200m"),
											"memory": resource.MustParse("256Mi"),
										},
										Requests: corev1.ResourceList{
											"cpu":    resource.MustParse("100m"),
											"memory": resource.MustParse("128Mi"),
										},
									},
								},
							},
						},
					},
				},
			},
			expected: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
								{
									Name:  "sidecar",
									Image: "sidecar:1.0.0",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											"cpu":    resource.MustParse("200m"),
											"memory": resource.MustParse("256Mi"),
										},
										Requests: corev1.ResourceList{
											"cpu":    resource.MustParse("100m"),
											"memory": resource.MustParse("128Mi"),
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			expectedErr: nil,
		},
		{
			name: "Remove sidecar container",
			original: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
								{
									Name:  "sidecar",
									Image: "sidecar:1.0.0",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											"cpu":    resource.MustParse("200m"),
											"memory": resource.MustParse("256Mi"),
										},
										Requests: corev1.ResourceList{
											"cpu":    resource.MustParse("100m"),
											"memory": resource.MustParse("128Mi"),
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			patch:       &redkeyv1.PartialStatefulSet{},
			expected:    redisStatefulSet,
			expectedErr: nil,
		},
		{
			name:     "Add volume and init container",
			original: redisStatefulSet,
			patch: &redkeyv1.PartialStatefulSet{
				Spec: &redkeyv1.PartialStatefulSetSpec{
					Template: &redkeyv1.PartialPodTemplateSpec{
						Spec: redkeyv1.PartialPodSpec{
							Volumes: []corev1.Volume{
								{
									Name: "data-override",
									VolumeSource: corev1.VolumeSource{
										EmptyDir: &corev1.EmptyDirVolumeSource{},
									},
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:    "init",
									Image:   "init:1.0.0",
									Command: []string{"init.sh"},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "data-override",
											MountPath: "/data",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:    "init",
									Image:   "init:1.0.0",
									Command: []string{"init.sh"},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "data-override",
											MountPath: "/data",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "data-override",
									VolumeSource: corev1.VolumeSource{
										EmptyDir: &corev1.EmptyDirVolumeSource{},
									},
								},
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			expectedErr: nil,
		},
		{
			name: "Remove volume and init container",
			original: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:    "init",
									Image:   "init:1.0.0",
									Command: []string{"init.sh"},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "data",
											MountPath: "/data",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			patch:       &redkeyv1.PartialStatefulSet{},
			expected:    redisStatefulSet,
			expectedErr: nil,
		},
		{
			name:     "Add tolerations, topology constraint and affinity",
			original: redisStatefulSet,
			patch: &redkeyv1.PartialStatefulSet{
				Spec: &redkeyv1.PartialStatefulSetSpec{
					Template: &redkeyv1.PartialPodTemplateSpec{
						Spec: redkeyv1.PartialPodSpec{
							Tolerations: []corev1.Toleration{
								{
									Key:      "key",
									Operator: "Equal",
									Value:    "value",
									Effect:   "NoSchedule",
								},
							},
							Affinity: &corev1.Affinity{
								NodeAffinity: &corev1.NodeAffinity{
									RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
										NodeSelectorTerms: []corev1.NodeSelectorTerm{
											{
												MatchExpressions: []corev1.NodeSelectorRequirement{
													{
														Key:      "topology.kubernetes.io/zone",
														Operator: "In",
														Values:   []string{"zone1", "zone2"},
													},
												},
											},
										},
									},
								},
							},
							TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
								{
									MaxSkew:           1,
									TopologyKey:       "topology.kubernetes.io/zone",
									WhenUnsatisfiable: "DoNotSchedule",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": "redis",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
							Tolerations: []corev1.Toleration{
								{
									Key:      "key",
									Operator: "Equal",
									Value:    "value",
									Effect:   "NoSchedule",
								},
							},
							Affinity: &corev1.Affinity{
								NodeAffinity: &corev1.NodeAffinity{
									RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
										NodeSelectorTerms: []corev1.NodeSelectorTerm{
											{
												MatchExpressions: []corev1.NodeSelectorRequirement{
													{
														Key:      "topology.kubernetes.io/zone",
														Operator: "In",
														Values:   []string{"zone1", "zone2"},
													},
												},
											},
										},
									},
								},
							},
							TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
								{
									MaxSkew:           1,
									TopologyKey:       "topology.kubernetes.io/zone",
									WhenUnsatisfiable: "DoNotSchedule",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": "redis",
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			expectedErr: nil,
		},
		{
			name: "Remove tolerations, topology constraint and affinity",
			original: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
					CreationTimestamp: metav1.Time{
						Time: time.Unix(1, 0),
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:            &replicas,
					PodManagementPolicy: appsv1.ParallelPodManagement,
					Selector: &metav1.LabelSelector{
						MatchLabels: defaultLabels,
					},
					ServiceName: "redkeycluster",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: defaultLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "redis",
									Image: "redis:6.0.15",
									Ports: []corev1.ContainerPort{
										{
											Name:          "client",
											ContainerPort: RedisCommPort,
										},
										{
											Name:          "gossip",
											ContainerPort: RedisGossPort,
										},
									},
									Command:        []string{"redis-server", "/conf/redis.conf"},
									LivenessProbe:  CreateProbe(15, 5),
									ReadinessProbe: CreateProbe(10, 5),
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{},
										Requests: corev1.ResourceList{},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/conf",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
											Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
										},
									},
								},
							},
							Tolerations: []corev1.Toleration{
								{
									Key:      "key",
									Operator: "Equal",
									Value:    "value",
									Effect:   "NoSchedule",
								},
							},
							Affinity: &corev1.Affinity{
								NodeAffinity: &corev1.NodeAffinity{
									RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
										NodeSelectorTerms: []corev1.NodeSelectorTerm{
											{
												MatchExpressions: []corev1.NodeSelectorRequirement{
													{
														Key:      "topology.kubernetes.io/zone",
														Operator: "In",
														Values:   []string{"zone1", "zone2"},
													},
												},
											},
										},
									},
								},
							},
							TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
								{
									MaxSkew:           1,
									TopologyKey:       "topology.kubernetes.io/zone",
									WhenUnsatisfiable: "DoNotSchedule",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": "redis",
										},
									},
								},
							},
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      3,
					ReadyReplicas: 3,
				},
			},
			patch:       &redkeyv1.PartialStatefulSet{},
			expected:    redisStatefulSet,
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret, err := ApplyStsOverride(tt.original, tt.patch)

			if tt.expectedErr != nil {
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.Equal(t, tt.expected, ret)
			}
		})
	}
}

func Test_ApplyServiceOverride(t *testing.T) {
	tests := []struct {
		name        string
		original    *corev1.Service
		patch       *redkeyv1.PartialService
		expected    *corev1.Service
		expectedErr error
	}{
		{
			name:        "Patch empty",
			original:    redisService,
			patch:       &redkeyv1.PartialService{},
			expected:    redisService,
			expectedErr: nil,
		},
		{
			name:     "Add labels and annotations",
			original: redisService,
			patch: &redkeyv1.PartialService{
				Metadata: metav1.ObjectMeta{
					Labels:      map[string]string{"new-label": "new-value"},
					Annotations: map[string]string{"new-annotation": "new-value"},
				},
			},
			expected: &corev1.Service{
				TypeMeta: metav1.TypeMeta{
					Kind: "Service", APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        "redkeycluster",
					Namespace:   "default",
					Labels:      map[string]string{RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis, "new-label": "new-value"},
					Annotations: map[string]string{"new-annotation": "new-value"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "client",
							Protocol: "TCP",
							Port:     RedisCommPort,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: RedisCommPort,
							},
						},
					},
					Selector:  map[string]string{RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis},
					ClusterIP: "None",
				},
			},
			expectedErr: nil,
		},
		{
			name:     "Add selector",
			original: redisService,
			patch: &redkeyv1.PartialService{
				Spec: &redkeyv1.PartialServiceSpec{
					Selector: map[string]string{"new-selector": "new-value"},
				},
			},
			expected: &corev1.Service{
				TypeMeta: metav1.TypeMeta{
					Kind: "Service", APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "client",
							Protocol: "TCP",
							Port:     RedisCommPort,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: RedisCommPort,
							},
						},
					},
					Selector:  map[string]string{RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis, "new-selector": "new-value"},
					ClusterIP: "None",
				},
			},
			expectedErr: nil,
		},
		{
			name: "Remove selector",
			original: &corev1.Service{
				TypeMeta: metav1.TypeMeta{
					Kind: "Service", APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "client",
							Protocol: "TCP",
							Port:     RedisCommPort,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: RedisCommPort,
							},
						},
					},
					Selector:  map[string]string{RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis, "new-selector": "new-value"},
					ClusterIP: "None",
				},
			},
			patch:       &redkeyv1.PartialService{},
			expected:    redisService,
			expectedErr: nil,
		},
		{
			name:     "Add port",
			original: redisService,
			patch: &redkeyv1.PartialService{
				Spec: &redkeyv1.PartialServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "prometheus",
							Protocol: "TCP",
							Port:     9090,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: 9090,
							},
						},
					},
				},
			},
			expected: &corev1.Service{
				TypeMeta: metav1.TypeMeta{
					Kind: "Service", APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "prometheus",
							Protocol: "TCP",
							Port:     9090,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: 9090,
							},
						},
						{
							Name:     "client",
							Protocol: "TCP",
							Port:     RedisCommPort,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: RedisCommPort,
							},
						},
					},
					Selector:  defaultLabels,
					ClusterIP: "None",
				},
			},
			expectedErr: nil,
		},
		{
			name: "Remove port",
			original: &corev1.Service{
				TypeMeta: metav1.TypeMeta{
					Kind: "Service", APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redkeycluster",
					Namespace: "default",
					Labels:    defaultLabels,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "client",
							Protocol: "TCP",
							Port:     RedisCommPort,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: RedisCommPort,
							},
						},
						{
							Name:     "prometheus",
							Protocol: "TCP",
							Port:     9090,
							TargetPort: intstr.IntOrString{Type: 0,
								IntVal: 9090,
							},
						},
					},
					Selector:  defaultLabels,
					ClusterIP: "None",
				},
			},
			patch:       &redkeyv1.PartialService{},
			expected:    redisService,
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret, err := ApplyServiceOverride(tt.original, tt.patch)

			if tt.expectedErr != nil {
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.Equal(t, tt.expected, ret)
			}
		})
	}
}

func Test_ApplyPodTemplateSpecOverride(t *testing.T) {
	tests := []struct {
		name        string
		original    corev1.PodTemplateSpec
		patch       corev1.PodTemplateSpec
		expected    *corev1.PodTemplateSpec
		expectedErr error
	}{
		{
			name:     "Patch empty",
			original: *podTemplateSpec,
			patch:    corev1.PodTemplateSpec{},
			expected: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{},
			},
			expectedErr: nil,
		},
		{
			name:     "Update container and volume",
			original: *podTemplateSpec,
			patch: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:6.0.16",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
									Items:                []corev1.KeyToPath{{Key: "redis1.conf", Path: "redis1.conf"}},
								},
							},
						},
					},
				},
			},
			expected: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:6.0.16",
							Ports: []corev1.ContainerPort{
								{
									Name:          "client",
									ContainerPort: RedisCommPort,
								},
								{
									Name:          "gossip",
									ContainerPort: RedisGossPort,
								},
							},
							Command:        []string{"redis-server", "/conf/redis.conf"},
							LivenessProbe:  CreateProbe(15, 5),
							ReadinessProbe: CreateProbe(10, 5),
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("20m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/conf",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
									Items:                []corev1.KeyToPath{{Key: "redis1.conf", Path: "redis1.conf"}},
								},
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			name:     "Add labels and annotations",
			original: *podTemplateSpec,
			patch: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"new-label": "new-value"},
					Annotations: map[string]string{"new-annotation": "new-value"},
				},
			},
			expected: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"new-label": "new-value", RedkeyClusterLabel: "redkeycluster", RedkeyClusterComponentLabel: common.ComponentLabelRedis},
					Annotations: map[string]string{"new-annotation": "new-value"},
				},
				Spec: corev1.PodSpec{},
			},
			expectedErr: nil,
		},
		{
			name:     "Add tolerations, topology constraint and affinity",
			original: *podTemplateSpec,
			patch: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:6.0.15",
							Ports: []corev1.ContainerPort{
								{
									Name:          "client",
									ContainerPort: RedisCommPort,
								},
								{
									Name:          "gossip",
									ContainerPort: RedisGossPort,
								},
							},
							Command:        []string{"redis-server", "/conf/redis.conf"},
							LivenessProbe:  CreateProbe(15, 5),
							ReadinessProbe: CreateProbe(10, 5),
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("20m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/conf",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
									Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "key",
							Operator: "Equal",
							Value:    "value",
							Effect:   "NoSchedule",
						},
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "topology.kubernetes.io/zone",
												Operator: "In",
												Values:   []string{"zone1", "zone2"},
											},
										},
									},
								},
							},
						},
					},
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
						{
							MaxSkew:           1,
							TopologyKey:       "topology.kubernetes.io/zone",
							WhenUnsatisfiable: "DoNotSchedule",
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": "redis",
								},
							},
						},
					},
				},
			},
			expected: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:6.0.15",
							Ports: []corev1.ContainerPort{
								{
									Name:          "client",
									ContainerPort: RedisCommPort,
								},
								{
									Name:          "gossip",
									ContainerPort: RedisGossPort,
								},
							},
							Command:        []string{"redis-server", "/conf/redis.conf"},
							LivenessProbe:  CreateProbe(15, 5),
							ReadinessProbe: CreateProbe(10, 5),
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("20m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/conf",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
									Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "key",
							Operator: "Equal",
							Value:    "value",
							Effect:   "NoSchedule",
						},
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "topology.kubernetes.io/zone",
												Operator: "In",
												Values:   []string{"zone1", "zone2"},
											},
										},
									},
								},
							},
						},
					},
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
						{
							MaxSkew:           1,
							TopologyKey:       "topology.kubernetes.io/zone",
							WhenUnsatisfiable: "DoNotSchedule",
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": "redis",
								},
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			name: "Delete tolerations, topology constraint and affinity",
			original: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: defaultLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:6.0.15",
							Ports: []corev1.ContainerPort{
								{
									Name:          "client",
									ContainerPort: RedisCommPort,
								},
								{
									Name:          "gossip",
									ContainerPort: RedisGossPort,
								},
							},
							Command:        []string{"redis-server", "/conf/redis.conf"},
							LivenessProbe:  CreateProbe(15, 5),
							ReadinessProbe: CreateProbe(10, 5),
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("20m"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/conf",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "redkeycluster"},
									Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "key",
							Operator: "Equal",
							Value:    "value",
							Effect:   "NoSchedule",
						},
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "topology.kubernetes.io/zone",
												Operator: "In",
												Values:   []string{"zone1", "zone2"},
											},
										},
									},
								},
							},
						},
					},
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
						{
							MaxSkew:           1,
							TopologyKey:       "topology.kubernetes.io/zone",
							WhenUnsatisfiable: "DoNotSchedule",
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": "redis",
								},
							},
						},
					},
				},
			},
			patch:       *podTemplateSpec,
			expected:    podTemplateSpec,
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret, err := ApplyPodTemplateSpecOverride(tt.original, tt.patch)

			if tt.expectedErr != nil {
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.Equal(t, tt.expected, ret)
			}
		})
	}
}
