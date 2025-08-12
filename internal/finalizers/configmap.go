// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package finalizer

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConfigMapCleanupFinalizer struct {
}

func (ef *ConfigMapCleanupFinalizer) DeleteMethod(ctx context.Context, redis *redkeyv1.RedKeyCluster, client client.Client) error {
	err := client.Delete(ctx, &corev1.ConfigMap{
		ObjectMeta: v1.ObjectMeta{Name: redis.GetName(), Namespace: redis.GetNamespace()},
	})
	return err
}

func (ef *ConfigMapCleanupFinalizer) GetId() string {
	return "redis.inditex.dev/configmap-cleanup"
}
