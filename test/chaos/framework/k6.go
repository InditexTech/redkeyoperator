// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	defaultK6Image   = "localhost:5001/redkey-k6:dev"
	k6StartupTimeout = 2 * time.Minute
	k6StopTimeout    = 30 * time.Second
	defaultK6VUs     = 10
	k6LogTailLines   = int64(200)
	// k6 runs with a very long duration so it keeps generating load until
	// the test explicitly stops it by deleting the deployment.
	k6RunDuration = "24h"
)

// K6LoadSelector returns the label selector for k6 load pods.
func K6LoadSelector() string {
	return "app=k6-load"
}

// GetK6Image returns the k6 image from environment or default.
func GetK6Image() string {
	if img := os.Getenv("K6_IMG"); img != "" {
		return img
	}
	return defaultK6Image
}

// StartK6LoadDeployment creates a k6 Deployment that generates continuous
// load against the Redis cluster. The deployment keeps running until
// explicitly stopped via StopK6Load. Returns the deployment name.
func StartK6LoadDeployment(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, vus int) (string, error) {
	if vus <= 0 {
		vus = defaultK6VUs
	}

	// Get Redis pod IPs for REDIS_HOSTS
	redisHosts, err := getRedisHosts(ctx, clientset, namespace, clusterName)
	if err != nil {
		return "", fmt.Errorf("failed to get redis hosts: %w", err)
	}

	deployName := fmt.Sprintf("k6-load-%s", clusterName)
	labels := map[string]string{
		"app":                 "k6-load",
		"redkey-cluster-name": clusterName,
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:  "k6",
							Image: GetK6Image(),
							Args: []string{
								"run",
								"/scripts/test-300k.js",
								"--duration", k6RunDuration,
								"--vus", fmt.Sprintf("%d", vus),
							},
							Env: []corev1.EnvVar{
								{
									Name:  "REDIS_HOSTS",
									Value: redisHosts,
								},
							},
						},
					},
				},
			},
		},
	}

	// Delete existing deployment if present
	propagation := metav1.DeletePropagationForeground
	_ = clientset.AppsV1().Deployments(namespace).Delete(ctx, deployName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})

	// Wait for deletion
	_ = wait.PollUntilContextTimeout(ctx, time.Second, k6StopTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
		return errors.IsNotFound(err), nil
	})

	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("failed to create k6 deployment: %w", err)
	}

	// Wait for at least one pod to be running
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, k6StartupTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: K6LoadSelector(),
		})
		if err != nil {
			return false, nil
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return deployName, fmt.Errorf("k6 deployment pod did not start: %w", err)
	}

	return deployName, nil
}

// StopK6Load deletes the k6 load deployment and waits for its pods to
// terminate. It is safe to call with an empty name.
func StopK6Load(ctx context.Context, clientset kubernetes.Interface, namespace, deployName string) error {
	if deployName == "" {
		return nil
	}

	propagation := metav1.DeletePropagationForeground
	err := clientset.AppsV1().Deployments(namespace).Delete(ctx, deployName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return wait.PollUntilContextTimeout(ctx, time.Second, k6StopTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}

// GetK6Logs returns the logs from a running k6 load pod.
func GetK6Logs(ctx context.Context, clientset kubernetes.Interface, namespace string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: K6LoadSelector(),
	})
	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no k6 pods found")
	}

	// Get logs from the first pod using proper log API
	pod := pods.Items[0]
	tailLines := k6LogTailLines
	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	req := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Sprintf("Pod %s running but failed to get logs: %v", pod.Name, err), nil
	}
	defer stream.Close()

	var buf strings.Builder
	if _, err := io.Copy(&buf, stream); err != nil {
		return fmt.Sprintf("Pod %s running but failed to read logs: %v", pod.Name, err), nil
	}

	return buf.String(), nil
}

// getRedisHosts returns stable redis host:port endpoints for k6.
func getRedisHosts(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("redkey-cluster-name=%s,redis.redkeycluster.operator/component=redis", clusterName),
	})
	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no redis pods found")
	}

	var hosts []string
	for _, pod := range pods.Items {
		if pod.Name != "" {
			hosts = append(hosts, fmt.Sprintf("%s.%s.%s.svc.cluster.local:6379", pod.Name, clusterName, namespace))
			continue
		}
		if pod.Status.PodIP != "" {
			hosts = append(hosts, fmt.Sprintf("%s:6379", pod.Status.PodIP))
		}
	}

	if len(hosts) == 0 {
		return "", fmt.Errorf("no redis pod IPs found")
	}

	return strings.Join(hosts, ","), nil
}
