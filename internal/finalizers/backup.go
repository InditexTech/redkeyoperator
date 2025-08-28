// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package finalizer

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type BackupFinalizer struct {
}

func (ef *BackupFinalizer) DeleteMethod(ctx context.Context, redis *redkeyv1.RedKeyCluster, client client.Client) error {
	// final backup before deletion
	return nil
}

func (ef *BackupFinalizer) GetId() string {
	return "redis.inditex.dev/rdb-backup"
}
