// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
)

const (
	robinScaleTimeout  = 2 * time.Minute
	scaleRetryTimeout  = 2 * time.Minute
	scaleRetryInterval = 3 * time.Second
)

// isNotReadyValidationError returns true when the Kubernetes API rejects a
// mutation because the cluster status is not 'Ready'. This is a transient
// condition during chaos: the cluster will eventually settle to Ready, at
// which point the scale request will be accepted.
func isNotReadyValidationError(err error) bool {
	if err == nil {
		return false
	}
	if !errors.IsInvalid(err) {
		return false
	}
	return strings.Contains(err.Error(), "Changing the number of primaries is not allowed unless the cluster is in 'Ready' status")
}

// DeleteRandomRedisPods deletes N random redis pods from the cluster.
func DeleteRandomRedisPods(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, count int, rng *rand.Rand) ([]string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list redis pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no redis pods found to delete")
	}

	// Limit count to available pods (never delete all)
	maxDelete := len(pods.Items) - 1
	if maxDelete < 1 {
		maxDelete = 1
	}
	if count > maxDelete {
		count = maxDelete
	}

	// Shuffle and pick N pods
	indices := rng.Perm(len(pods.Items))

	var deleted []string
	for i := 0; i < count && i < len(indices); i++ {
		pod := pods.Items[indices[i]]
		if err := clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return deleted, fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
		}
		deleted = append(deleted, pod.Name)
	}

	return deleted, nil
}

// DeleteRobinPods requests deletion of all robin pods for the cluster.
// It only verifies that the Kubernetes delete calls succeed.
func DeleteRobinPods(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RobinPodsSelector(clusterName),
	})
	if err != nil {
		return fmt.Errorf("failed to list robin pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil
	}

	for _, pod := range pods.Items {
		if err := clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete robin pod %s: %w", pod.Name, err)
		}
	}

	return nil
}

// ScaleCluster scales the Redis cluster to the specified number of primaries.
// It retries on conflict errors and on webhook validation errors that reject
// the change because the cluster is not yet in 'Ready' status, which is a
// transient condition during chaos operations.
func ScaleCluster(ctx context.Context, dc dynamic.Interface, namespace, clusterName string, primaries int32) error {
	return wait.PollUntilContextTimeout(ctx, scaleRetryInterval, scaleRetryTimeout, true, func(ctx context.Context) (bool, error) {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return ScaleRedkeyCluster(ctx, dc, namespace, clusterName, primaries)
		})
		if err == nil {
			return true, nil
		}
		// If the webhook rejected the update because the cluster is not Ready,
		// keep polling—the operator will eventually reconcile the cluster back
		// to Ready status.
		if isNotReadyValidationError(err) {
			return false, nil
		}
		return false, err
	})
}

// ScaleRobinDown scales robin deployment to 0 replicas.
func ScaleRobinDown(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) error {
	return scaleRobinDeploymentNative(ctx, clientset, namespace, clusterName, 0)
}

// ScaleRobinUp scales robin deployment to 1 replica.
func ScaleRobinUp(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) error {
	return scaleRobinDeploymentNative(ctx, clientset, namespace, clusterName, 1)
}

// scaleRobinDeploymentNative finds and scales the robin deployment using native client-go.
func scaleRobinDeploymentNative(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, replicas int32) error {
	robinDepName := clusterName + "-robin"

	dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, robinDepName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) && replicas == 0 {
			return nil
		}
		return fmt.Errorf("failed to get robin deployment %s: %w", robinDepName, err)
	}

	dep.Spec.Replicas = ptr.To(replicas)
	if _, err := clientset.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to scale robin deployment %s: %w", robinDepName, err)
	}

	if replicas == 0 {
		return wait.PollUntilContextTimeout(ctx, 2*time.Second, robinScaleTimeout, true, func(ctx context.Context) (bool, error) {
			dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, robinDepName, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return true, nil
				}
				return false, nil
			}

			selector, err := deploymentPodSelector(dep)
			if err != nil {
				return false, err
			}

			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return false, nil
			}

			return dep.Status.Replicas == 0 && dep.Status.ReadyReplicas == 0 && len(pods.Items) == 0, nil
		})
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, robinScaleTimeout, true, func(ctx context.Context) (bool, error) {
		dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, robinDepName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return dep.Status.ReadyReplicas >= replicas, nil
	})
}

// CorruptSlotOwnership corrupts slot ownership by removing a slot and assigning it inconsistently.
// This requires operator and robin to be scaled down first.
func CorruptSlotOwnership(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, slot int) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return fmt.Errorf("failed to list redis pods: %w", err)
	}

	if len(pods.Items) < 2 {
		return fmt.Errorf("need at least 2 pods for slot corruption")
	}

	// Get node IDs
	nodeIDs := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		stdout, _, err := RemoteCommand(ctx, namespace, pod.Name, "redis-cli cluster nodes | grep myself | awk '{ print $1 }'")
		if err != nil {
			return fmt.Errorf("failed to get node ID from %s: %w", pod.Name, err)
		}
		nodeIDs = append(nodeIDs, trimNewline(stdout))
	}

	// Delete slot from all nodes
	for _, pod := range pods.Items {
		stdout, stderr, err := RemoteCommand(ctx, namespace, pod.Name, fmt.Sprintf("redis-cli cluster delslots %d", slot))
		if err != nil {
			return fmt.Errorf("failed to delslots %d on %s: %w (stdout=%q stderr=%q)", slot, pod.Name, err, trimNewline(stdout), trimNewline(stderr))
		}
	}

	// Assign slot to different nodes inconsistently (first two nodes)
	_, _, err = RemoteCommand(ctx, namespace, pods.Items[0].Name, fmt.Sprintf("redis-cli cluster setslot %d node %s", slot, nodeIDs[0]))
	if err != nil {
		return fmt.Errorf("failed to setslot on first node: %w", err)
	}

	_, _, err = RemoteCommand(ctx, namespace, pods.Items[1].Name, fmt.Sprintf("redis-cli cluster setslot %d node %s", slot, nodeIDs[1]))
	if err != nil {
		return fmt.Errorf("failed to setslot on second node: %w", err)
	}

	return nil
}

// SetSlotMigrating puts a slot in migrating/importing state across nodes.
// This requires operator and robin to be scaled down first.
func SetSlotMigrating(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, slot int) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return fmt.Errorf("failed to list redis pods: %w", err)
	}

	if len(pods.Items) < 2 {
		return fmt.Errorf("need at least 2 pods for slot migration corruption")
	}

	// Get node IDs
	nodeIDs := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		stdout, _, err := RemoteCommand(ctx, namespace, pod.Name, "redis-cli cluster nodes | grep myself | awk '{ print $1 }'")
		if err != nil {
			return fmt.Errorf("failed to get node ID from %s: %w", pod.Name, err)
		}
		nodeIDs = append(nodeIDs, trimNewline(stdout))
	}

	// Set slot as migrating from node0 to node1
	_, _, err = RemoteCommand(ctx, namespace, pods.Items[0].Name, fmt.Sprintf("redis-cli cluster setslot %d migrating %s", slot, nodeIDs[1]))
	if err != nil {
		return fmt.Errorf("failed to set slot migrating: %w", err)
	}

	// Set slot as importing on node1 from node0
	_, _, err = RemoteCommand(ctx, namespace, pods.Items[1].Name, fmt.Sprintf("redis-cli cluster setslot %d importing %s", slot, nodeIDs[0]))
	if err != nil {
		return fmt.Errorf("failed to set slot importing: %w", err)
	}

	return nil
}

// ForcePrimaryToReplica forces a primary node to become a replica of another primary.
// This requires operator and robin to be scaled down first.
func ForcePrimaryToReplica(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName, podName string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RedisPodsSelector(clusterName),
	})
	if err != nil {
		return fmt.Errorf("failed to list redis pods: %w", err)
	}

	// Find another primary to replicate
	var targetNodeID string
	for _, pod := range pods.Items {
		if pod.Name == podName {
			continue
		}
		stdout, _, err := RemoteCommand(ctx, namespace, pod.Name, "redis-cli cluster nodes | grep myself | awk '{ print $1 }'")
		if err != nil {
			continue
		}
		targetNodeID = trimNewline(stdout)
		break
	}

	if targetNodeID == "" {
		return fmt.Errorf("no target node found for replication")
	}

	// Delete all slots from the pod
	stdout, stderr, err := RemoteCommand(ctx, namespace, podName, "redis-cli cluster flushslots")
	if err != nil {
		return fmt.Errorf("failed to flushslots on %s: %w (stdout=%q stderr=%q)", podName, err, trimNewline(stdout), trimNewline(stderr))
	}

	// Make it replicate the target
	_, _, err = RemoteCommand(ctx, namespace, podName, fmt.Sprintf("redis-cli cluster replicate %s", targetNodeID))
	if err != nil {
		return fmt.Errorf("failed to replicate: %w", err)
	}

	return nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
