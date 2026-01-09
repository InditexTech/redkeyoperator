// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
)

// Label selectors for Redis operator components.
const (
	redisComponentLabel = "redis.redkeycluster.operator/component"
	clusterNameLabel    = "redkey-cluster-name"
)

// RedisPodsSelector returns the label selector for Redis pods in a cluster.
func RedisPodsSelector(clusterName string) string {
	return fmt.Sprintf("%s=%s,%s=redis", clusterNameLabel, clusterName, redisComponentLabel)
}

// RobinPodsSelector returns the label selector for Robin pods in a cluster.
func RobinPodsSelector(clusterName string) string {
	return fmt.Sprintf("%s=%s,%s=robin", clusterNameLabel, clusterName, redisComponentLabel)
}

// OperatorPodsSelector returns the label selector for operator pods.
func OperatorPodsSelector() string {
	return "control-plane=redkey-operator"
}

// Cached REST config for RemoteCommand.
var (
	cachedConfig *rest.Config
	configOnce   sync.Once
	configErr    error
)

// getCachedConfig returns a cached REST config, creating it once.
func getCachedConfig() (*rest.Config, error) {
	configOnce.Do(func() {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
		}
		cachedConfig, configErr = clientcmd.BuildConfigFromFlags("", kubeconfig)
	})
	return cachedConfig, configErr
}

// RemoteCommand executes a command in a pod and returns stdout, stderr, error.
// Uses a cached REST config for efficiency.
func RemoteCommand(ctx context.Context, namespace, podName, command string) (string, string, error) {
	config, err := getCachedConfig()
	if err != nil {
		return "", "", fmt.Errorf("get config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", "", fmt.Errorf("create clientset: %w", err)
	}

	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}

	request := clientset.CoreV1().RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
			Command: []string{"/bin/sh", "-c", command},
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", request.URL())
	if err != nil {
		return "", "", fmt.Errorf("create executor: %w", err)
	}

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: buf,
		Stderr: errBuf,
		Tty:    false,
	})
	if err != nil {
		return buf.String(), errBuf.String(), err
	}
	return buf.String(), errBuf.String(), nil
}

// GetPodLogs returns the last N lines of logs from pods matching the selector.
func GetPodLogs(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, tailLines int64) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", err
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found matching %s", labelSelector)
	}

	// Get logs from the first pod
	pod := pods.Items[0]
	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}

	req := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream logs from %s: %w", pod.Name, err)
	}
	defer stream.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, stream); err != nil {
		return "", fmt.Errorf("read logs: %w", err)
	}

	return buf.String(), nil
}
