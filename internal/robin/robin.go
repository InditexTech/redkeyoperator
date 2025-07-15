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
	"reflect"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/inditextech/redisoperator/internal/common"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/redis"
	"gopkg.in/yaml.v3"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
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

type Status struct {
	Status string `yaml:"status"`
}

type ClusterCheck struct {
	Errors   []string `yaml:"errors"`
	Warnings []string `yaml:"warnings"`
}

type ClusterNodes struct {
	Nodes []Node `yaml:"nodes"`
}

type Node struct {
	Name       string      `yaml:"name"`
	Id         string      `yaml:"id"`
	Ip         string      `yaml:"ip"`
	Flags      string      `yaml:"flags"`
	Slots      []SlotRange `yaml:"slots"`
	MasterId   string      `yaml:"masterId"`
	Failures   int         `yaml:"failures"`
	Sent       int         `yaml:"sent"`
	Recv       int         `yaml:"recv"`
	LinkStatus string      `yaml:"linkStatus"`
}

type SlotRange struct {
	Start int `yaml:"start"`
	End   int `yaml:"end"`
}

type Robin struct {
	Pod    *corev1.Pod
	Logger logr.Logger
}

// Gets Robin initialized with the existing pod.
func NewRobin(ctx context.Context, client ctrlClient.Client, redisCluster *redisv1.RedisCluster, logger logr.Logger) (Robin, error) {
	componentLabel := kubernetes.GetStatefulSetSelectorLabel(ctx, client, redisCluster)
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedisClusterLabel: redisCluster.Name,
			componentLabel:          common.ComponentLabelRobin,
		},
	)

	robin := Robin{}
	robin.Logger = logger

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
		robin.Pod = pods.Items[0].DeepCopy()
	case 0:
		return robin, fmt.Errorf("robin pod not found")
	default:
		return robin, fmt.Errorf("more than one Robin pods where found, which is not allowed")
	}

	return robin, nil
}

// Updates configuration in Robin ConfigMap with the new state from the RedisCluster object.
func PersistRobinStatut(ctx context.Context, client ctrlClient.Client, redisCluster *redisv1.RedisCluster) error {
	cmap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}, cmap)
	if err != nil {
		return err
	}
	var config Configuration
	if err := yaml.Unmarshal([]byte(cmap.Data["application-configmap.yml"]), &config); err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}
	config.Redis.Cluster.Status = redisCluster.Status.Status
	confUpdated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}
	cmap.Data["application-configmap.yml"] = string(confUpdated)
	if err = client.Update(ctx, cmap); err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}

	return nil
}

func (r *Robin) GetStatus() (string, error) {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/rediscluster/status"

	body, err := doSimpleGet(url)
	if err != nil {
		return "", fmt.Errorf("getting Robin status: %w", err)
	}

	var status Status
	err = json.Unmarshal(body, &status)
	if err != nil {
		return "", fmt.Errorf("parsing Robin status response: %w", err)
	}

	return status.Status, nil
}

func (r *Robin) SetStatus(status string) error {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/rediscluster/status"

	var statusParam Status
	statusParam.Status = status
	payload, err := json.Marshal(statusParam)
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

func (r *Robin) ClusterCheck() (bool, []string, []string, error) {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/cluster/check"

	body, err := doSimpleGet(url)
	if err != nil {
		return false, nil, nil, fmt.Errorf("getting Robin status: %w", err)
	}

	var clusterCheck ClusterCheck
	err = json.Unmarshal(body, &clusterCheck)
	if err != nil {
		return false, nil, nil, fmt.Errorf("parsing Robin status response: %w", err)
	}

	checkResult := true
	if len(clusterCheck.Errors) != 0 || len(clusterCheck.Warnings) != 0 {
		checkResult = false
	}
	return checkResult, clusterCheck.Errors, clusterCheck.Warnings, nil
}

func (r *Robin) GetClusterNodes() (ClusterNodes, error) {
	url := "http://" + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + "/v1/cluster/nodes"
	var clusterNodes ClusterNodes

	body, err := doSimpleGet(url)
	if err != nil {
		return clusterNodes, fmt.Errorf("getting cluster nodes: %w", err)
	}

	err = json.Unmarshal(body, &clusterNodes)
	if err != nil {
		return clusterNodes, fmt.Errorf("parsing cluster nodes: %w", err)
	}
	return clusterNodes, nil
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
