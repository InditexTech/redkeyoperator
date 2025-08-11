// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	r "github.com/inditextech/redkeyoperator/internal/redis"

	ginkgo "github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"strconv"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newClient(rc *redkeyv1.RedKeyCluster, s *runtime.Scheme) client.Client {
	return fake.NewClientBuilder().WithObjects(rc).WithScheme(s).Build()
}

func newScheme() *runtime.Scheme {
	sb := redkeyv1.SchemeBuilder
	s, err := sb.Build()
	if err != nil {
		fmt.Println(err)
	}
	return s
}

func newReconciler(redis *redkeyv1.RedKeyCluster, recorder record.EventRecorder) *RedKeyClusterReconciler {
	ctrl.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))

	var defaultReplicas int32 = 3

	scheme := newScheme()

	reconciler := &RedKeyClusterReconciler{
		Client:                      newClient(redis, scheme),
		Scheme:                      scheme,
		Log:                         ctrl.Log.WithName("controllers").WithName("redkeycluster"),
		MaxConcurrentReconciles:     10,
		ConcurrentMigrate:           3,
		Recorder:                    recorder,
		GetReadyNodesFunc:           mockReadyNodes(make(map[string]*redkeyv1.RedisNode)),
		FindExistingStatefulSetFunc: mockStatefulSet(newStatefulSet(redis, defaultReplicas)),
		FindExistingConfigMapFunc:   mockConfigMap(newConfigMap()),
	}

	return reconciler
}

func newContext() context.Context {
	return context.Background()
}

func newRequest(rc *redkeyv1.RedKeyCluster) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: rc.Namespace,
			Name:      rc.Name,
		},
	}
}

func newRedKeyCluster() *redkeyv1.RedKeyCluster {
	om := metav1.ObjectMeta{
		Name:      "redkey-cluster",
		Namespace: "unittest",
		Labels: map[string]string{
			"label-key": "label-value",
		},
	}
	return &redkeyv1.RedKeyCluster{
		ObjectMeta: om,
		Status: redkeyv1.RedKeyClusterStatus{
			Status:     redkeyv1.StatusReady,
			Conditions: []metav1.Condition{},
		},
		Spec: redkeyv1.RedKeyClusterSpec{
			Replicas: 3,
			Config:   r.MapToConfigString(r.MergeWithDefaultConfig(nil, false, 0)),
			Image:    "redis-operator:0.3.0",
			Resources: &corev1.ResourceRequirements{
				Limits:   newLimits(),
				Requests: newRequests(),
			},
		},
	}
}

func newRequests() corev1.ResourceList {
	return corev1.ResourceList{}
}

func newLimits() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("16Gi"),
	}
}

func mockReadyNodes(nodes map[string]*redkeyv1.RedisNode) func(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (map[string]*redkeyv1.RedisNode, error) {
	return func(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (map[string]*redkeyv1.RedisNode, error) {
		return nodes, nil
	}
}

func newReadyNodes(amount int) map[string]*redkeyv1.RedisNode {
	readyNodes := make(map[string]*redkeyv1.RedisNode)
	for i := 0; i < amount; i++ {
		readyNodes[strconv.Itoa(i)] = &redkeyv1.RedisNode{}
	}
	return readyNodes
}

func mockStatefulSet(sset *v1.StatefulSet) func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
		return sset, nil
	}
}

func newStatefulSet(redis *redkeyv1.RedKeyCluster, numReplicas int32) *v1.StatefulSet {
	req := newRequest(redis)
	spec := redis.Spec
	labels := make(map[string]string)
	sset, _ := r.CreateStatefulSet(newContext(), req, spec, labels)
	sset.Spec.Replicas = &numReplicas
	for k := range sset.Spec.Template.Spec.Containers {
		sset.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU] = *redis.Spec.Resources.Limits.Cpu()
		sset.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory] = *redis.Spec.Resources.Limits.Memory()
		sset.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceCPU] = *redis.Spec.Resources.Requests.Cpu()
		sset.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceMemory] = *redis.Spec.Resources.Requests.Memory()
	}
	return sset
}

func mockConfigMap(configMap *corev1.ConfigMap) func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
		return configMap, nil
	}
}

func newConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{}
}
