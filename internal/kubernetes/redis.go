// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/redis"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func FindExistingStatefulSet(ctx context.Context, client client.Client, req ctrl.Request) (*v1.StatefulSet, error) {
	statefulset := &v1.StatefulSet{}
	err := client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, statefulset)
	if err != nil {
		return nil, err
	}
	return statefulset, nil
}

func FindExistingConfigMap(ctx context.Context, client client.Client, req ctrl.Request) (*corev1.ConfigMap, error) {
	cmap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

func GetStatefulSetSelectorLabel(ctx context.Context, client client.Client, redisCluster *redkeyv1.RedKeyCluster) string {
	statefulset, err := FindExistingStatefulSet(ctx, client, reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      redisCluster.Name,
		Namespace: redisCluster.Namespace,
	}})
	if err != nil {
		if errors.IsNotFound(err) {
			return redis.RedisClusterComponentLabel
		}
		return ""
	}
	if statefulset.Spec.Template.Labels[redis.RedisClusterComponentLabel] != "" {
		// new label
		return redis.RedisClusterComponentLabel
	} else {
		return "app"
	}
}

func FindExistingDeployment(ctx context.Context, client client.Client, req ctrl.Request) (*v1.Deployment, error) {
	deployment := &v1.Deployment{}
	err := client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, deployment)
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func FindExistingPodDisruptionBudget(ctx context.Context, client client.Client, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	pdb := &pv1.PodDisruptionBudget{}
	err := client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, pdb)
	if err != nil {
		return nil, err
	}
	return pdb, nil
}
