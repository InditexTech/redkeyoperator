// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

// NOTE: This file is adapted from test/e2e/framework/namespace.go for chaos tests.
package framework

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	defaultNamespaceWait = 100 * time.Second
	defaultNamespacePoll = 1 * time.Second
)

// CreateNamespace creates a namespace with a GenerateName prefix and waits for it to be ready.
func CreateNamespace(ctx context.Context, clientset kubernetes.Interface, prefix string) (*corev1.Namespace, error) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
		},
	}
	created, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	err = wait.PollUntilContextTimeout(ctx, defaultNamespacePoll, defaultNamespaceWait, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Namespaces().Get(ctx, created.Name, metav1.GetOptions{})
		return err == nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait for namespace %s to be ready: %w", created.Name, err)
	}
	return created, nil
}

// DeleteNamespace tears down everything in the namespace, including
// RedkeyCluster CRs with finalizers, then deletes the namespace itself.
func DeleteNamespace(ctx context.Context, clientset kubernetes.Interface, dc dynamic.Interface, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}

	// 1) Remove any RedkeyCluster CRs so their finalizers don't stall namespace deletion
	rcList, err := dc.Resource(RedkeyClusterGVR).Namespace(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("list RedkeyClusters in %s: %w", ns.Name, err)
	}

	if rcList != nil {
		for _, item := range rcList.Items {
			name := item.GetName()

			// Strip finalizers with retry
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				rc, err := dc.Resource(RedkeyClusterGVR).Namespace(ns.Name).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				rc.SetFinalizers(nil)
				_, err = dc.Resource(RedkeyClusterGVR).Namespace(ns.Name).Update(ctx, rc, metav1.UpdateOptions{})
				return err
			})
			if err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("removing finalizers from %s/%s: %w", ns.Name, name, err)
			}

			// Delete the CR immediately
			if err := dc.Resource(RedkeyClusterGVR).Namespace(ns.Name).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("deleting RedkeyCluster %s/%s: %w", ns.Name, name, err)
			}
		}
	}

	// 2) Delete the namespace
	if err := clientset.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %s: %w", ns.Name, err)
	}

	// 3) Wait for the namespace to actually disappear
	err = wait.PollUntilContextTimeout(ctx, defaultNamespacePoll, defaultNamespaceWait, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		return errors.IsNotFound(err), nil
	})
	if err != nil {
		return fmt.Errorf("namespace %s should be gone: %w", ns.Name, err)
	}
	return nil
}
