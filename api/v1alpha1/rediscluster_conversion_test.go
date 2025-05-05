// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"testing"

	v1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_toV1(t *testing.T) {
	tests := []struct {
		name        string
		src         *RedisCluster
		expected    *v1.RedisCluster
		expectedErr error
	}{
		{
			name: "Case base",
			src: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{
						{
							Start: 0,
							End:   16383,
						},
					},
				},
			},
			expected: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Robin:                &v1.RobinSpec{},
					Override:             &v1.RedisClusterOverrideSpec{},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			name: "Extra annotations",
			src: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
					Annotations: map[string]string{
						"inditex.com/nodes-extra-annotations": `{"annotation1": "value1"}`,
					},
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{
						{
							Start: 0,
							End:   16383,
						},
					},
				},
			},
			expected: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-rediscluster",
					Namespace:   "default",
					Annotations: map[string]string{},
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Robin:                &v1.RobinSpec{},
					Override: &v1.RedisClusterOverrideSpec{
						StatefulSet: &appsv1.StatefulSet{
							Spec: appsv1.StatefulSetSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Annotations: map[string]string{
											"annotation1": "value1",
										},
									},
								},
							},
						},
					},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			name: "Extra labels",
			src: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
					Annotations: map[string]string{
						"inditex.com/nodes-extra-labels": `{"label1": "value1"}`,
					},
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{
						{
							Start: 0,
							End:   16383,
						},
					},
				},
			},
			expected: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-rediscluster",
					Namespace:   "default",
					Annotations: map[string]string{},
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Robin:                &v1.RobinSpec{},
					Override: &v1.RedisClusterOverrideSpec{
						StatefulSet: &appsv1.StatefulSet{
							Spec: appsv1.StatefulSetSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Labels: map[string]string{
											"label1": "value1",
										},
									},
								},
							},
						},
					},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := &v1.RedisCluster{}
			err := tt.src.toV1(dst)

			if tt.expectedErr != nil {
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.Equal(t, tt.expected, dst)
			}
		})
	}
}

func Test_fromV1(t *testing.T) {
	tests := []struct {
		name        string
		src         *v1.RedisCluster
		expected    *RedisCluster
		expectedErr error
	}{
		{
			name: "Case base",
			src: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Override: &v1.RedisClusterOverrideSpec{
						StatefulSet: &appsv1.StatefulSet{},
					},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expected: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "node1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{},
				},
			},
			expectedErr: nil,
		},
		{
			name: "Extra annotations",
			src: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Override: &v1.RedisClusterOverrideSpec{
						StatefulSet: &appsv1.StatefulSet{
							Spec: appsv1.StatefulSetSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Annotations: map[string]string{
											"annotation1": "value1",
										},
									},
								},
							},
						},
					},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expected: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
					Annotations: map[string]string{
						"inditex.com/nodes-extra-annotations": `{"annotation1":"value1"}`,
					},
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "node1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{},
				},
			},
			expectedErr: nil,
		},
		{
			name: "Extra annotations",
			src: &v1.RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
				},
				Spec: v1.RedisClusterSpec{
					Auth: v1.RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Override: &v1.RedisClusterOverrideSpec{
						StatefulSet: &appsv1.StatefulSet{
							Spec: appsv1.StatefulSetSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Labels: map[string]string{
											"label1": "value2",
										},
									},
								},
							},
						},
					},
				},
				Status: v1.RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*v1.RedisNode{
						"node1": {
							Name:      "node1",
							IP:        "",
							IsMaster:  false,
							ReplicaOf: "",
						},
					},
				},
			},
			expected: &RedisCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "redis.inditex.com/v1alpha1",
					Kind:       "RedisCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rediscluster",
					Namespace: "default",
					Annotations: map[string]string{
						"inditex.com/nodes-extra-labels": `{"label1":"value2"}`,
					},
				},
				Spec: RedisClusterSpec{
					Auth: RedisAuth{
						SecretName: "secret",
					},
					Version:              "6.0.0",
					Replicas:             3,
					ReplicasPerMaster:    1,
					Image:                "redis:6.0.0",
					Ephemeral:            false,
					Backup:               false,
					Storage:              "1Gi",
					PurgeKeysOnRebalance: false,
					Config:               "config",
					StorageClassName:     "storage",
					DeletePVC:            false,
					AccessModes:          []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
				Status: RedisClusterStatus{
					Status: "OK",
					Nodes: map[string]*RedisNode{
						"node1": {
							NodeName: "node1",
							NodeID:   "node1",
							IP:       "",
						},
					},
					Slots: []*SlotRange{},
				},
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := &RedisCluster{}
			err := dst.fromV1(tt.src)

			if tt.expectedErr != nil {
				assert.Equal(t, tt.expectedErr, err)
			} else {
				assert.Equal(t, tt.expected, dst)
			}
		})
	}
}
