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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	defaultK6Image    = "localhost:5001/redkey-k6:dev"
	k6JobTimeout      = 30 * time.Minute
	k6StartupTimeout  = 2 * time.Minute
	defaultK6VUs      = 10
	k6ScriptConfigMap = "k6-scripts"
)

// GetK6Image returns the k6 image from environment or default.
func GetK6Image() string {
	if img := os.Getenv("K6_IMG"); img != "" {
		return img
	}
	return defaultK6Image
}

// StartK6LoadJob creates and starts a k6 Job for load testing.
// Returns the job name.
func StartK6LoadJob(ctx context.Context, clientset kubernetes.Interface, namespace, clusterName string, duration time.Duration, vus int) (string, error) {
	if vus <= 0 {
		vus = defaultK6VUs
	}

	// Get Redis pod IPs for REDIS_HOSTS
	redisHosts, err := getRedisHosts(ctx, clientset, namespace, clusterName)
	if err != nil {
		return "", fmt.Errorf("failed to get redis hosts: %w", err)
	}

	jobName := fmt.Sprintf("k6-load-%s", clusterName)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                 "k6-load",
						"redkey-cluster-name": clusterName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:  "k6",
							Image: GetK6Image(),
							Args: []string{
								"run",
								"/scripts/test-300k.js",
								"--duration", formatDuration(duration),
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

	// Delete existing job if present
	propagation := metav1.DeletePropagationForeground
	_ = clientset.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})

	// Wait for deletion
	_ = wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		return errors.IsNotFound(err), nil
	})

	if _, err := clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("failed to create k6 job: %w", err)
	}

	// Wait for job pod to start
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, k6StartupTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=k6-load",
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
		return jobName, fmt.Errorf("k6 job pod did not start: %w", err)
	}

	return jobName, nil
}

// WaitForK6JobCompletion waits for the k6 job to complete successfully.
func WaitForK6JobCompletion(ctx context.Context, clientset kubernetes.Interface, namespace, jobName string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = k6JobTimeout
	}

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, fmt.Errorf("k6 job not found")
			}
			return false, nil
		}

		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				return false, fmt.Errorf("k6 job failed: %s", condition.Message)
			}
		}

		return false, nil
	})
}

// DeleteK6Job deletes the k6 job and its pods.
func DeleteK6Job(ctx context.Context, clientset kubernetes.Interface, namespace, jobName string) error {
	if jobName == "" {
		return nil
	}

	propagation := metav1.DeletePropagationForeground
	err := clientset.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// GetK6JobLogs returns the logs from the k6 job pod.
func GetK6JobLogs(ctx context.Context, clientset kubernetes.Interface, namespace, jobName string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=k6-load,job-name=%s", jobName),
	})
	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		// Try alternative label selector
		pods, err = clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=k6-load",
		})
		if err != nil {
			return "", err
		}
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no k6 pods found")
	}

	// Get logs from the first pod using proper log API
	pod := pods.Items[0]
	tailLines := int64(1000)
	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	req := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Sprintf("Pod %s completed but failed to get logs: %v", pod.Name, err), nil
	}
	defer stream.Close()

	var buf strings.Builder
	if _, err := io.Copy(&buf, stream); err != nil {
		return fmt.Sprintf("Pod %s completed but failed to read logs: %v", pod.Name, err), nil
	}

	return buf.String(), nil
}

// getRedisHosts returns a comma-separated list of redis host:port for k6.
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
		if pod.Status.PodIP != "" {
			hosts = append(hosts, fmt.Sprintf("%s:6379", pod.Status.PodIP))
		}
	}

	if len(hosts) == 0 {
		return "", fmt.Errorf("no redis pod IPs found")
	}

	return strings.Join(hosts, ","), nil
}

// formatDuration formats a duration for k6 (e.g., "10m", "1h30m").
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}
