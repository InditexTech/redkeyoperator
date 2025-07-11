// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package robin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/common"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/redis"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	StatusInitializing = "Initializing"
	StatutsConfiguring = "Configuring"
	StatusReady        = "Ready"
	StatusError        = "Error"
	StatutsUpgrading   = "Upgrading"
	StatusScalingDown  = "ScalingDown"
	StatusScalingUp    = "ScalingUp"
	StatusMaintenance  = "Maintenance"
	StatusUnknown      = "Unknown"
	Port               = 8080
)

type Robin struct {
	pod    *corev1.Pod
	logger logr.Logger
}

// Gets Robin initialized with the existing pod.
func GetRobin(ctx context.Context, client ctrlClient.Client, redisCluster *redisv1.RedisCluster, logger logr.Logger) (Robin, error) {
	componentLabel := kubernetes.GetStatefulSetSelectorLabel(ctx, client, redisCluster)
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel: redisCluster.Name,
			componentLabel:          common.ComponentLabelRobin,
		},
	)

	robin := Robin{}
	robin.logger = logger

	pods := &corev1.PodList{}
	err := client.List(ctx, pods, &ctrlClient.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return robin, err
	}

	switch len(pods.Items) {
	case 1:
		robin.pod = pods.Items[0].DeepCopy()
	case 0:
		return robin, fmt.Errorf("robin pod not found")
	default:
		return robin, fmt.Errorf("more than one Robin pods where found, which is not allowed")
	}

	return robin, nil
}

func (r *Robin) GetStatus() (string, error) {
	return "Initializing", nil
}

func (r *Robin) SetStatus(status string) error {
	// set Robin status
	url := "http://" + r.pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/rediscluster/status"
	payload, err := json.Marshal(map[string]any{
		"status": status,
	})
	if err != nil {
		return fmt.Errorf("setting Robin status: %w", err)
	}

	body, err := doPut(url, payload)
	if err != nil {
		return fmt.Errorf("setting Robin status: %w", err)
	}
	r.logger.Info("Robin status updated", "status", status, "response body", string(body))

	return nil
}

func (r *Robin) GetReplicas() (int, error) {
	return 3, nil
}

func (r *Robin) SetReplicas(replicas int) error {
	return nil
}

// TODO struct with the data gotten from check call
func (r *Robin) GetClusterCheck() (bool, error) {
	return true, nil
}

func (r *Robin) GetClusterNodes() (string, error) {
	return "", nil
}

func (r *Robin) ClusterFix() error {
	return nil
}

func (r *Robin) ClusterResetNode(nodeIndex int) error {
	return nil
}

func doPut(url string, payload []byte) ([]byte, error) {
	client := &http.Client{}

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}
