// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"

	typeCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
)

// Get the clientSet for kubernetes conection
func getClientSet() (*kubernetes.Clientset, *rest.Config, error) {
	// Get the KUBECONFIG environment variable
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// Get config file of kubernetes
		kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	// Create the rest.Config for kubernetes conection
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading config file of kubernetes: %v", err)
	}
	// Create the client for kubernetes conection
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating client of kubernetes: %v", err)
	}
	return clientset, config, nil
}

// Execute a command with exec by pod
func remoteCommand(namespace string, podName string, command string) (string, string, error) {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	executor, err := remoteCommandExecutor(namespace, podName, command)
	if err != nil {
		return "", "", err
	}

	// Connect this process' std{in,out,err} to the remote shell process.
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: buf,
		Stderr: errBuf,
		Tty:    false,
	})
	if err != nil {
		return "", "", err
	}
	return buf.String(), errBuf.String(), nil
}

// Create the executor that allow execute commands in pods in a cluster of kubernetes
func remoteCommandExecutor(namespace, podName, command string) (remotecommand.Executor, error) {
	_, config, err := getClientSet()
	if err != nil {
		return nil, fmt.Errorf("error creating client of kubernetes: %v", err)
	}

	coreV1Client, err := typeCoreV1.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	request := coreV1Client.RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			TypeMeta: metav1.TypeMeta{},
			Stdout:   true,
			Stderr:   true,
			TTY:      false,
			Command:  []string{"/bin/sh", "-c", command},
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", request.URL())
	if err != nil {
		return nil, err
	}
	return executor, nil
}
