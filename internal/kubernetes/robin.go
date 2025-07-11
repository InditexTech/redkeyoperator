// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// Updates configuration in Robin ConfigMap with the new state from the RedisCluster object.
func PersistRobinStatut(ctx context.Context, client client.Client, redisCluster *redisv1.RedisCluster) error {
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
