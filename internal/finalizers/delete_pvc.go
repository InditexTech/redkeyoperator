// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package finalizer

import (
	"context"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeletePVCFinalizer struct {
}

func (ef *DeletePVCFinalizer) DeleteMethod(ctx context.Context, redis *redisv1.RedisCluster, c client.Client) error {
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.DeleteAllOf(ctx, pvc, client.InNamespace(redis.Namespace), client.MatchingLabels{"redis-cluster-name": redis.Name})
	return err
}

func (ef *DeletePVCFinalizer) GetId() string {
	return "redis.inditex.dev/delete-pvc"
}
