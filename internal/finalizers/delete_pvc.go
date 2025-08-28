// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package finalizer

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeletePVCFinalizer struct {
}

func (ef *DeletePVCFinalizer) DeleteMethod(ctx context.Context, redis *redkeyv1.RedKeyCluster, c client.Client) error {
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.DeleteAllOf(ctx, pvc, client.InNamespace(redis.Namespace), client.MatchingLabels{"redkey-cluster-name": redis.Name})
	return err
}

func (ef *DeletePVCFinalizer) GetId() string {
	return "redis.inditex.dev/delete-pvc"
}
