// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

// Package framework provides helper functions for chaos tests.
package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultChaosReadyTimeout = 10 * time.Minute
	pollInterval             = 2 * time.Second
)

// WaitForChaosReady waits for the Redis cluster to be fully healthy.
// Checks: CR status == Ready, redis-cli --cluster check passes, no fail/migrating states.
func WaitForChaosReady(ctx context.Context, dc dynamic.Interface, clientset kubernetes.Interface, namespace, clusterName string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultChaosReadyTimeout
	}

	var lastReason string
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		// When the context is cancelled or expired, avoid overwriting
		// lastReason so we preserve the real diagnostic from the
		// previous poll tick. All API calls below would fail with a
		// context error, which is not useful for debugging.
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		// 1. Check CR status
		cluster, err := GetRedkeyCluster(ctx, dc, namespace, clusterName)
		if err != nil {
			if ctx.Err() == nil {
				lastReason = fmt.Sprintf("error getting cluster: %v", err)
			}
			return false, nil
		}

		if cluster.Status.Status != redkeyv1.StatusReady {
			lastReason = fmt.Sprintf("CR status is %q (want Ready)", cluster.Status.Status)
			return false, nil
		}

		// 2. List redis pods
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: RedisPodsSelector(clusterName),
		})
		if err != nil {
			if ctx.Err() == nil {
				lastReason = fmt.Sprintf("error listing pods: %v", err)
			}
			return false, nil
		}

		if len(pods.Items) == 0 {
			lastReason = "pod count is 0"
			return false, nil
		}

		// 3. Verify the pod count matches what the spec expects.
		// This prevents a false-positive Ready when the operator hasn't yet
		// processed a spec.primaries change (race between CR update and reconcile).
		expected := cluster.Spec.NodesNeeded()
		if len(pods.Items) != expected {
			lastReason = fmt.Sprintf("pod count %d != expected %d (spec.primaries=%d)", len(pods.Items), expected, cluster.Spec.Primaries)
			return false, nil
		}

		// 4. For each running pod, verify cluster health
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				lastReason = fmt.Sprintf("pod %s phase is %s (want Running)", pod.Name, pod.Status.Phase)
				return false, nil
			}

			if ctx.Err() != nil {
				return false, nil
			}

			if !clusterCheckPasses(ctx, namespace, pod.Name) {
				if ctx.Err() == nil {
					lastReason = fmt.Sprintf("redis-cli --cluster check failed on pod %s", pod.Name)
				}
				return false, nil
			}

			if clusterNodesHasFailure(ctx, namespace, pod.Name) {
				if ctx.Err() == nil {
					lastReason = fmt.Sprintf("cluster nodes failure detected on pod %s", pod.Name)
				}
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil && lastReason != "" {
		return fmt.Errorf("WaitForChaosReady(%s/%s): last check: %s: %w", namespace, clusterName, lastReason, err)
	}
	return err
}

// clusterCheckPasses runs redis-cli --cluster check and returns true if it succeeds.
func clusterCheckPasses(ctx context.Context, namespace, podName string) bool {
	stdout, _, err := RemoteCommand(ctx, namespace, podName, "redis-cli --cluster check localhost:6379")
	if err != nil {
		return false
	}
	return !strings.Contains(stdout, "[ERR]")
}

// clusterNodesHasFailure checks if any node is in fail state or has migrating/importing slots.
func clusterNodesHasFailure(ctx context.Context, namespace, podName string) bool {
	stdout, _, err := RemoteCommand(ctx, namespace, podName, "redis-cli cluster nodes")
	if err != nil {
		return true
	}
	return strings.Contains(stdout, "fail") || strings.Contains(stdout, "->") || strings.Contains(stdout, "<-")
}

// AssertAllSlotsAssigned verifies that all 16384 slots are assigned.
func AssertAllSlotsAssigned(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return err
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no redis pods found")
	}

	stdout, _, err := RemoteCommand(ctx, namespace, pods.Items[0].Name, "redis-cli cluster info")
	if err != nil {
		return fmt.Errorf("failed to get cluster info: %w", err)
	}

	if !strings.Contains(stdout, "cluster_slots_ok:16384") {
		return fmt.Errorf("not all slots assigned: %s", stdout)
	}

	return nil
}

// AssertNoNodesInFailState verifies no nodes are in fail state.
func AssertNoNodesInFailState(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return err
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no redis pods found")
	}

	for _, pod := range pods.Items {
		stdout, _, err := RemoteCommand(ctx, namespace, pod.Name, "redis-cli cluster nodes")
		if err != nil {
			return fmt.Errorf("failed to get cluster nodes from %s: %w", pod.Name, err)
		}

		if strings.Contains(stdout, "fail") {
			return fmt.Errorf("node in fail state detected in pod %s: %s", pod.Name, stdout)
		}
	}

	return nil
}
