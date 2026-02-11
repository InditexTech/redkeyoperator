// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package finalizer

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Finalizer interface {
	DeleteMethod(context.Context, *redkeyv1.RedkeyCluster, client.Client) error
	GetId() string
}
