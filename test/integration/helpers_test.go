// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	redisv1 "github.com/inditextech/redkeyoperator/api/v1beta1"
	"github.com/inditextech/redkeyoperator/internal/controller"
)

const clusterLabel = "redkey.inditex.dev/cluster"

func newReconciler() *controller.RedkeyClusterReconciler {
	return &controller.RedkeyClusterReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
	}
}

func reconcileCluster(ctx context.Context, name types.NamespacedName) {
	r := newReconciler()
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: name})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func newTestCluster(name, namespace string) *redisv1.RedkeyCluster {
	return &redisv1.RedkeyCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: redisv1.RedkeyClusterSpec{
			Ephemeral: true,
			Primaries: 3,
		},
	}
}

// newTestConfig creates a RedkeyClusterConfig with the given sequence and configPhase.
// The ownerReference is NOT set here because envtest requires the owner UID, which
// is only available after the cluster is created. Use setConfigOwner after cluster creation.
func newTestConfig(clusterName, namespace string, seq int, configPhase string) *redisv1.RedkeyClusterConfig {
	return &redisv1.RedkeyClusterConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", clusterName, seq),
			Namespace: namespace,
			Labels: map[string]string{
				clusterLabel: clusterName,
			},
			Annotations: map[string]string{
				"redkey.inditex.dev/cluster-generation": strconv.FormatInt(1, 10),
			},
		},
		Spec: redisv1.RedkeyClusterConfigSpec{
			Sequence:  seq,
			Primaries: 3,
			Ephemeral: true,
		},
		Status: redisv1.RedkeyClusterConfigStatus{
			ConfigPhase: configPhase,
		},
	}
}

// updateConfigStatus updates the status subresource of a RedkeyClusterConfig,
// ensuring that required fields (Nodes) are initialised to satisfy CRD validation.
func updateConfigStatus(ctx context.Context, config *redisv1.RedkeyClusterConfig) {
	if config.Status.Nodes == nil {
		config.Status.Nodes = map[string]*redisv1.RedisNode{}
	}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, config)).To(Succeed())
}

// createConfigWithPhase creates a RedkeyClusterConfig and sets its status via the status subresource.
func createConfigWithPhase(ctx context.Context, clusterName, namespace string, seq int, configPhase string) {
	config := newTestConfig(clusterName, namespace, seq, "")
	ExpectWithOffset(1, k8sClient.Create(ctx, config)).To(Succeed())

	// Status must be set via the status subresource
	config.Status.ConfigPhase = configPhase
	updateConfigStatus(ctx, config)
}

// listConfigs returns all configs for the given cluster sorted by sequence ascending.
func listConfigs(ctx context.Context, clusterName, namespace string) []redisv1.RedkeyClusterConfig {
	var configList redisv1.RedkeyClusterConfigList
	ExpectWithOffset(1, k8sClient.List(ctx, &configList,
		client.InNamespace(namespace),
		client.MatchingLabels{clusterLabel: clusterName},
	)).To(Succeed())

	items := configList.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].Spec.Sequence < items[j].Spec.Sequence
	})
	return items
}

// deleteCluster deletes a RedkeyCluster and all its configs.
func deleteCluster(ctx context.Context, name, namespace string) {
	cluster := &redisv1.RedkeyCluster{}
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	if err := k8sClient.Get(ctx, nn, cluster); err == nil {
		ExpectWithOffset(1, k8sClient.Delete(ctx, cluster)).To(Succeed())
	}

	// Clean up any configs not garbage-collected (envtest doesn't run GC)
	configs := listConfigs(ctx, name, namespace)
	for i := range configs {
		_ = k8sClient.Delete(ctx, &configs[i])
	}
}
