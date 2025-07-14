// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package robin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
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

// Configuration is the top-level configuration struct.
type Configuration struct {
	Metadata map[string]string `yaml:"metadata"`
	Redis    RedisConfig       `yaml:"redis"`
}

// RedisConfig groups all Redis related configuration.
type RedisConfig struct {
	Standalone bool                  `yaml:"standalone"`
	Reconciler RedisReconcilerConfig `yaml:"reconciler"`
	Cluster    RedisClusterConfig    `yaml:"cluster"`
	Metrics    RedisMetricsConfig    `yaml:"metrics"`
}

// RedisOperatorConfig holds operator-level Redis configuration.
type RedisReconcilerConfig struct {
	IntervalSeconds                 int `yaml:"interval_seconds"`
	OperationCleanupIntervalSeconds int `yaml:"operation_cleanup_interval_seconds"`
}

// RedisClusterConfig holds cluster-level Redis configuration.
type RedisClusterConfig struct {
	Namespace                string        `yaml:"namespace"`
	Name                     string        `yaml:"name"`
	Replicas                 int           `yaml:"replicas"`
	ReplicasPerMaster        int           `yaml:"replicas_per_master"`
	Status                   string        `yaml:"status"`
	Ephemeral                bool          `yaml:"ephemeral"`
	HealthProbePeriodSeconds int           `yaml:"health_probe_interval_seconds"`
	HealingTimeSeconds       int           `yaml:"healing_time_seconds"`
	MaxRetries               int           `yaml:"max_retries"`
	BackOff                  time.Duration `yaml:"back_off"`
}

// RedisMetricsConfig holds metrics-related Redis configuration.
type RedisMetricsConfig struct {
	IntervalSeconds int      `yaml:"interval_seconds"`
	RedisInfoKeys   []string `yaml:"redis_info_keys"`
}

type ResponseStatus struct {
	Status string `yaml:"status"`
}

type Robin struct {
	Pod    *corev1.Pod
	Logger logr.Logger
}

func (r *Robin) GetStatus() (string, error) {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/rediscluster/status"
	
	body, err := doSimpleGet(url)
	if err != nil {
		return "", fmt.Errorf("getting Robin status: %w", err)
	}

	var status ResponseStatus
	err = json.Unmarshal(body, &status)
	if err != nil {
		return "", fmt.Errorf("parsing Robin status response: %w", err)
	}

	return status.Status, nil
}

func (r *Robin) SetStatus(status string) error {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/rediscluster/status"

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
	r.Logger.Info("Robin status updated", "status", status, "response body", string(body))

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

func doSimpleGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
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

// Compares two configurations, excluding `Redis.Cluster.Status` value.
func CompareConfigurations(a, b *Configuration) bool {
	a2 := new(Configuration)
	*a2 = *a
	a2.Redis.Cluster.Status = b.Redis.Cluster.Status
	return reflect.DeepEqual(a2, b)
}
