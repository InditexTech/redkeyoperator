// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
)

// nolint:unused
// log is for logging in this package.
var redisclusterlog = logf.Log.WithName("rediscluster-resource")

// SetupRedisClusterWebhookWithManager registers the webhook for RedisCluster in the manager.
func SetupRedisClusterWebhookWithManager(mgr ctrl.Manager) error {
	redisclusterlog.Info("Setting up RedisCluster webhook with manager")
	return ctrl.NewWebhookManagedBy(mgr).For(&redisv1.RedisCluster{}).
		Complete()
}
