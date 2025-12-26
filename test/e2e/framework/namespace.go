// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
)

const (
	defaultNamespaceWait = 100 * time.Second
	defaultNamespacePoll = 1 * time.Second
)

// CreateNamespace creates a namespace with a GenerateName prefix and waits for it to be ready
func CreateNamespace(ctx context.Context, c client.Client, prefix string) (*corev1.Namespace, error) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
		},
	}
	if err := c.Create(ctx, ns); err != nil {
		return nil, err
	}

	err := wait.PollUntilContextTimeout(ctx, defaultNamespacePoll, defaultNamespaceWait, true, func(ctx context.Context) (bool, error) {
		var tmp corev1.Namespace
		if err := c.Get(ctx, client.ObjectKey{Name: ns.Name}, &tmp); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait for namespace %s to be ready: %w", ns.Name, err)
	}
	return ns, nil
}

// DeleteNamespace tears down everything in the namespace, including
// RedkeyCluster CRs with finalizers, then deletes the namespace itself.
func DeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	// 1) Remove any RedkeyCluster CRs so their finalizers don't stall namespace deletion
	var rcList redkeyv1.RedkeyClusterList
	if err := c.List(ctx, &rcList, &client.ListOptions{Namespace: ns.Name}); err != nil {
		return err
	}

	for i := range rcList.Items {
		name := rcList.Items[i].Name
		namespace := rcList.Items[i].Namespace

		// Strip finalizers with retry
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			rc := &redkeyv1.RedkeyCluster{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, rc); err != nil {
				return err
			}
			rc.Finalizers = nil
			return c.Update(ctx, rc)
		})
		if err != nil {
			return fmt.Errorf("removing finalizers from %s/%s: %w", namespace, name, err)
		}

		// delete the CR immediately
		if err := c.Delete(ctx, &redkeyv1.RedkeyCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}); err != nil {
			return fmt.Errorf("deleting RedkeyCluster %s/%s: %w", namespace, name, err)
		}
	}

	// 2) Delete the namespace
	if err := c.Delete(ctx, ns); err != nil {
		return fmt.Errorf("deleting namespace %s: %w", ns.Name, err)
	}

	// 3) Wait for the namespace to actually disappear
	err := wait.PollUntilContextTimeout(ctx, defaultNamespacePoll, defaultNamespaceWait, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, types.NamespacedName{Name: ns.Name}, &corev1.Namespace{})
		if err != nil {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("namespace %s should be gone: %w", ns.Name, err)
	}
	return nil
}
