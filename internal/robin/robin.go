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
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/inditextech/redkeyoperator/internal/common"
	"github.com/inditextech/redkeyoperator/internal/kubernetes"
	"github.com/inditextech/redkeyoperator/internal/redis"
	"gopkg.in/yaml.v3"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	StatusInitializing  = "Initializing"
	StatutsConfiguring  = "Configuring"
	StatusReady         = "Ready"
	StatusError         = "Error"
	StatutsUpgrading    = "Upgrading"
	StatusScalingDown   = "ScalingDown"
	StatusScalingUp     = "ScalingUp"
	StatusMaintenance   = "Maintenance"
	StatusNoReconciling = "NoReconciling"
	StatusUnknown       = "Unknown"
	Port                = 8080

	EndpointProtocolPrefix  = "http://"
	EndpointStatus          = "/v1/redkeycluster/status"
	EndpointReplicas        = "/v1/redkeycluster/replicas"
	EndpointClusterCheck    = "/v1/cluster/check"
	EndpointClusterNodes    = "/v1/cluster/nodes"
	EndpointClusterFix      = "/v1/cluster/fix"
	EndpointClusterMove     = "/v1/cluster/move"
	EndpointClusterReset    = "/v1/cluster/reset/"
	EndpointClusterRecreate = "/v1/cluster/recreate"
	EndpointClusterStatus   = "/v1/cluster/status"
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
	Cluster    RedkeyClusterConfig   `yaml:"cluster"`
	Metrics    RedisMetricsConfig    `yaml:"metrics"`
}

// RedkeyOperatorConfig holds operator-level Redis configuration.
type RedisReconcilerConfig struct {
	IntervalSeconds                 int `yaml:"interval_seconds"`
	OperationCleanupIntervalSeconds int `yaml:"operation_cleanup_interval_seconds"`
}

// RedkeyClusterConfig holds cluster-level Redis configuration.
type RedkeyClusterConfig struct {
	Namespace                string        `yaml:"namespace"`
	Name                     string        `yaml:"name"`
	Primaries                int           `yaml:"primaries"`
	ReplicasPerPrimary       int           `yaml:"replicas_per_primary"`
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
	Name       string `yaml:"name"`
	Id         string `yaml:"id"`
	Ip         string `yaml:"ip"`
	Role       string `yaml:"role"`
	PrimaryId  string `yaml:"primaryId"`
	Failures   int    `yaml:"failures"`
	Sent       int    `yaml:"sent"`
	Recv       int    `yaml:"recv"`
	LinkStatus string `yaml:"linkStatus"`
}

type SlotRange struct {
	Start int `yaml:"start"`
	End   int `yaml:"end"`
}

type ClusterReplicas struct {
	Primaries          int `json:"primaries"`
	ReplicasPerPrimary int `json:"replicas_per_primary"`
}

type MoveSlots struct {
	NodeIndexFrom string `json:"from"`
	NodeIndexTo   string `json:"to"`
	NumSlots      int    `json:"slots"`
}

type MoveSlotsStatus struct {
	Status string `yaml:"status"`
}

type Robin struct {
	Pod    *corev1.Pod
	Logger logr.Logger
}

// Gets Robin initialized from a RedkeyCluster.
func NewRobin(ctx context.Context, client ctrlClient.Client, redkeyCluster *redkeyv1.RedkeyCluster, logger logr.Logger) (Robin, error) {
	componentLabel := kubernetes.GetStatefulSetSelectorLabel(ctx, client, redkeyCluster)
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			redis.RedkeyClusterLabel: redkeyCluster.Name,
			componentLabel:           common.ComponentLabelRobin,
		},
	)

	robin := Robin{}
	robin.Logger = logger

	pods := &corev1.PodList{}
	err := client.List(ctx, pods, &ctrlClient.ListOptions{
		Namespace:     redkeyCluster.Namespace,
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

func (r *Robin) GetStatus() (string, error) {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointStatus

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
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointStatus

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

func (r *Robin) SetAndPersistRobinStatus(ctx context.Context, client ctrlClient.Client, redkeyCluster *redkeyv1.RedkeyCluster, newStatus string) error {
	err := r.SetStatus(newStatus)
	if err != nil {
		return err
	}
	return PersistRobinStatus(ctx, client, redkeyCluster, newStatus)
}

func (r *Robin) GetReplicas() (int, int, error) {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointReplicas

	body, err := doSimpleGet(url)
	if err != nil {
		return 0, 0, fmt.Errorf("getting Robin status: %w", err)
	}

	var clusterReplicas ClusterReplicas
	err = json.Unmarshal(body, &clusterReplicas)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing Robin status response: %w", err)
	}

	return clusterReplicas.Primaries, clusterReplicas.ReplicasPerPrimary, nil
}

func (r *Robin) SetReplicas(clusterReplicas int, clusterReplicasPerPrimary int) error {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointReplicas

	var replicas ClusterReplicas
	replicas.Primaries = clusterReplicas
	replicas.ReplicasPerPrimary = clusterReplicasPerPrimary
	payload, err := json.Marshal(replicas)
	if err != nil {
		return fmt.Errorf("setting Robin status: %w", err)
	}

	body, err := doPut(url, payload)
	if err != nil {
		return fmt.Errorf("setting Robin status: %w", err)
	}
	r.Logger.Info("Robin cluster primaries and replicas updated", "primaries", replicas.Primaries, "replicas per primary", replicas.ReplicasPerPrimary, "response body", string(body))

	return nil
}

func (r *Robin) ClusterCheck() (bool, []string, []string, error) {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterCheck

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
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterNodes
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
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterFix

	var payload []byte
	body, err := doPut(url, payload)
	if err != nil {
		return fmt.Errorf("cluster fix: %w", err)
	}
	r.Logger.Info("Asked to Robin to fix the cluster", "response body", string(body))

	return nil
}

func (r *Robin) ClusterResetNode(nodeIndex int) error {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterReset + strconv.Itoa(nodeIndex)

	var payload []byte
	body, err := doPut(url, payload)
	if err != nil {
		return fmt.Errorf("reset node: %w", err)
	}
	r.Logger.Info("Asked to Robin to reset node", "node index", nodeIndex, "response body", string(body))

	return nil
}

func (r *Robin) MoveSlots(nodeIndexFrom int, nodeIndexTo int, numSlots int) (bool, error) {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterMove

	var moveParam MoveSlots
	moveParam.NodeIndexFrom = strconv.Itoa(nodeIndexFrom)
	moveParam.NodeIndexTo = strconv.Itoa(nodeIndexTo)
	moveParam.NumSlots = numSlots
	payload, err := json.Marshal(moveParam)
	if err != nil {
		return false, fmt.Errorf("moving slots: %w", err)
	}

	body, err := doPut(url, payload)
	if err != nil {
		return false, fmt.Errorf("moving slots: %w", err)
	}
	r.Logger.Info("Moving slots", "from", nodeIndexFrom, "to", nodeIndexTo, "slots", numSlots, "response body", string(body))

	var status MoveSlotsStatus
	err = json.Unmarshal(body, &status)
	if err != nil {
		return false, fmt.Errorf("parsing Robin response: %w", err)
	}

	if status.Status == "Completed" {
		r.Logger.Info("Moving slots completed", "from", nodeIndexFrom, "to", nodeIndexTo, "slots", numSlots)
		return true, nil
	} else {
		r.Logger.Info("Moving slots still in progress", "from", nodeIndexFrom, "to", nodeIndexTo, "slots", numSlots)
		return false, nil
	}
}

func (r *Robin) ClusterRecreate() error {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterRecreate

	var payload []byte
	body, err := doPut(url, payload)
	if err != nil {
		return fmt.Errorf("cluster recreate: %w", err)
	}
	r.Logger.Info("Asked to Robin to recreate the cluster", "response body", string(body))

	return nil
}

func (r *Robin) GetClusterStatus() (string, error) {
	url := EndpointProtocolPrefix + r.Pod.Status.PodIP + ":" + strconv.Itoa(Port) + EndpointClusterStatus

	body, err := doSimpleGet(url)
	if err != nil {
		return "", fmt.Errorf("getting Robin cluster status: %w", err)
	}

	var status Status
	err = json.Unmarshal(body, &status)
	if err != nil {
		return "", fmt.Errorf("parsing Robin cluster status response: %w", err)
	}

	return status.Status, nil
}

func doSimpleGet(url string) ([]byte, error) {
	client := &http.Client{
		Timeout: time.Second * 10,
	}

	resp, err := client.Get(url)
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
	client := &http.Client{
		Timeout: time.Second * 10,
	}

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

	// Normalize nil vs empty slice for RedisInfoKeys. This is needed because comparing a nil slice with an empty slice returns false.
	if len(b.Redis.Metrics.RedisInfoKeys) == 0 {
		a2.Redis.Metrics.RedisInfoKeys = []string{}
	}

	a2.Redis.Cluster.Status = b.Redis.Cluster.Status
	a2.Redis.Cluster.Primaries = b.Redis.Cluster.Primaries
	a2.Redis.Cluster.ReplicasPerPrimary = b.Redis.Cluster.ReplicasPerPrimary
	return reflect.DeepEqual(a2, b)
}

func (cn *ClusterNodes) GetPrimaryNodes() []*Node {
	primaryNodes := make([]*Node, 0)
	for _, node := range cn.Nodes {
		if strings.Contains(node.Role, "primary") {
			primaryNodes = append(primaryNodes, &node)
		}
	}
	return primaryNodes
}

func (cn *ClusterNodes) GetReplicaNodes() []*Node {
	replicaNodes := make([]*Node, 0)
	for _, node := range cn.Nodes {
		if !strings.Contains(node.Role, "primary") {
			replicaNodes = append(replicaNodes, &node)
		}
	}
	return replicaNodes
}

// Updates configuration in Robin ConfigMap with the new status.
func PersistRobinStatus(ctx context.Context, client ctrlClient.Client, redkeyCluster *redkeyv1.RedkeyCluster, newStatus string) error {
	cmap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: redkeyCluster.Name + "-robin", Namespace: redkeyCluster.Namespace}, cmap)
	if err != nil {
		return err
	}
	var config Configuration
	if err := yaml.Unmarshal([]byte(cmap.Data["application-configmap.yml"]), &config); err != nil {
		return fmt.Errorf("persist Robin status: %w", err)
	}
	config.Redis.Cluster.Status = newStatus
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

// Updates configuration in Robin ConfigMap with the new replicas.
func PersistRobinReplicas(ctx context.Context, client ctrlClient.Client, redkeyCluster *redkeyv1.RedkeyCluster, replicas int, replicasPerPrimary int) error {
	cmap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: redkeyCluster.Name + "-robin", Namespace: redkeyCluster.Namespace}, cmap)
	if err != nil {
		return err
	}
	var config Configuration
	if err := yaml.Unmarshal([]byte(cmap.Data["application-configmap.yml"]), &config); err != nil {
		return fmt.Errorf("persist Robin replicas: %w", err)
	}
	config.Redis.Cluster.Primaries = replicas
	config.Redis.Cluster.ReplicasPerPrimary = replicasPerPrimary
	confUpdated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("persist Robin replicas: %w", err)
	}
	cmap.Data["application-configmap.yml"] = string(confUpdated)
	if err = client.Update(ctx, cmap); err != nil {
		return fmt.Errorf("persist Robin replicas: %w", err)
	}

	return nil
}
