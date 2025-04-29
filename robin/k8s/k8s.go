// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func GetClientSet() (*kubernetes.Clientset, error) {
	var kubernetesclient *kubernetes.Clientset

	restConfig, err := getConfig().ClientConfig()
	if err != nil {
		panic("Unable to get client config.")
	}

	kubernetesclient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		panic("Unable to configure kubernetes client.")
	}

	return kubernetesclient, nil
}

func getConfig() clientcmd.ClientConfig {
	configLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		configLoadingRules,
		&clientcmd.ConfigOverrides{})
}
