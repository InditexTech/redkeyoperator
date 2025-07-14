// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/common"
	"github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/robin"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Gets Robin initialized with the existing pod.
func GetRobin(ctx context.Context, client ctrlClient.Client, redisCluster *redisv1.RedisCluster, logger logr.Logger) (robin.Robin, error) {
	componentLabel := GetStatefulSetSelectorLabel(ctx, client, redisCluster)
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel: redisCluster.Name,
			componentLabel:          common.ComponentLabelRobin,
		},
	)

	robin := robin.Robin{}
	robin.Logger = logger

	pods := &corev1.PodList{}
	err := client.List(ctx, pods, &ctrlClient.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return robin, err
	}

	switch len(pods.Items) {
	case 1:
		robin.Pod = pods.Items[0].DeepCopy()
	case 0:
		return robin, fmt.Errorf("robin pod not found")
	default:
		return robin, fmt.Errorf("more than one Robin pods where found, which is not allowed")
	}

	return robin, nil
}

// Updates configuration in Robin ConfigMap with the new state from the RedisCluster object.
func PersistRobinStatut(ctx context.Context, client client.Client, redisCluster *redisv1.RedisCluster) error {
	cmap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}, cmap)
	if err != nil {
		return err
	}
	var config robin.Configuration
	if err := yaml.Unmarshal([]byte(cmap.Data["application-configmap.yml"]), &config); err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}
	config.Redis.Cluster.Status = redisCluster.Status.Status
	confUpdated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}
	cmap.Data["application-configmap.yml"] = string(confUpdated)
	if err = client.Update(ctx, cmap); err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}

	return nil
}
