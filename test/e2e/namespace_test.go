// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	RedisNamespace = "redis-e2e-test"
)

func ensureNamespaceExistsOrCreate(nsName types.NamespacedName) error {
	err := k8sClient.Create(context.Background(), &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: RedisNamespace,
		},
	})
	// ignore failure if namespace already exists, fail for any other errors
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
