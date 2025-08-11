// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
)

// nolint:unused
// log is for logging in this package.
var redkeyclusterlog = logf.Log.WithName("redkeycluster-resource")

// SetupRedKeyClusterWebhookWithManager registers the webhook for RedkeyCluster in the manager.
func SetupRedKeyClusterWebhookWithManager(mgr ctrl.Manager) error {
	redkeyclusterlog.Info("Setting up RedKeyCluster webhook with manager")
	return ctrl.NewWebhookManagedBy(mgr).For(&redkeyv1.RedKeyCluster{}).
		Complete()
}
