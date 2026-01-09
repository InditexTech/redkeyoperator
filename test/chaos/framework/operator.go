// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	operatorDeploymentName = "redis-operator"
	operatorScaleTimeout   = 2 * time.Minute

	defaultOperatorImage = "localhost:5001/redkey-operator:dev"
)

// GetOperatorImage returns the operator image from environment or default.
func GetOperatorImage() string {
	if img := os.Getenv("OPERATOR_IMAGE"); img != "" {
		return img
	}
	return defaultOperatorImage
}

// ScaleOperatorDown scales the operator deployment to 0 replicas and waits for termination.
func ScaleOperatorDown(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	return scaleDeploymentNative(ctx, clientset, namespace, operatorDeploymentName, 0)
}

// ScaleOperatorUp scales the operator deployment to 1 replica and waits for readiness.
func ScaleOperatorUp(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	return scaleDeploymentNative(ctx, clientset, namespace, operatorDeploymentName, 1)
}

// DeleteOperatorPod deletes the operator pod (the deployment will recreate it).
func DeleteOperatorPod(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: OperatorPodsSelector(),
	})
	if err != nil {
		return fmt.Errorf("failed to list operator pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil
	}

	for _, pod := range pods.Items {
		if err := clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete operator pod %s: %w", pod.Name, err)
		}
	}

	// Wait for at least one operator pod to be ready again
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, operatorScaleTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: OperatorPodsSelector(),
		})
		if err != nil {
			return false, nil
		}

		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) {
				return true, nil
			}
		}
		return false, nil
	})
}

// scaleDeploymentNative scales a deployment to the desired replica count using native client-go.
func scaleDeploymentNative(ctx context.Context, clientset kubernetes.Interface, namespace, name string, replicas int32) error {
	dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	dep.Spec.Replicas = ptr.To(replicas)
	if _, err := clientset.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to scale deployment %s: %w", name, err)
	}

	if replicas == 0 {
		return waitForDeploymentScaleDownNative(ctx, clientset, namespace, name)
	}
	return waitForDeploymentReadyNative(ctx, clientset, namespace, name, replicas)
}

// waitForDeploymentScaleDownNative waits until no pods exist for the deployment.
func waitForDeploymentScaleDownNative(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, operatorScaleTimeout, true, func(ctx context.Context) (bool, error) {
		dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}
		return dep.Status.Replicas == 0, nil
	})
}

// waitForDeploymentReadyNative waits until the deployment has the desired ready replicas.
func waitForDeploymentReadyNative(ctx context.Context, clientset kubernetes.Interface, namespace, name string, replicas int32) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, operatorScaleTimeout, true, func(ctx context.Context) (bool, error) {
		dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return dep.Status.ReadyReplicas >= replicas, nil
	})
}

// isPodReady returns true if all containers in the pod are ready.
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
