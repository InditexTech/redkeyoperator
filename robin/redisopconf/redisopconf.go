// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package redisopconf

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/inditextech/redisoperator/robin/util"
	"gopkg.in/yaml.v3"
)

// RedisOperatorConfig holds operator-level Redis configuration.
type RedisOperatorConfig struct {
	CollectionPeriodSeconds int `yaml:"collection_interval_seconds"`
}

// RedisClusterConfig holds cluster-level Redis configuration.
type RedisClusterConfig struct {
	Namespace                string        `yaml:"namespace"`
	ServiceName              string        `yaml:"service_name"`
	Replicas                 int           `yaml:"replicas"`
	HealthProbePeriodSeconds int           `yaml:"health_probe_interval_seconds"`
	HealingTimeSeconds       int           `yaml:"healing_time_seconds"`
	LabelSelector            string        `yaml:"label_selector"`
	MaxRetries               int           `yaml:"max_retries"`
	BackOff                  time.Duration `yaml:"back_off"`
}

// RedisMetricsConfig holds metrics-related Redis configuration.
type RedisMetricsConfig struct {
	RedisInfoKeys []string `yaml:"redis_info_keys"`
}

// RedisConfig groups all Redis related configuration.
type RedisConfig struct {
	Operator RedisOperatorConfig `yaml:"operator"`
	Cluster  RedisClusterConfig  `yaml:"cluster"`
	Metrics  RedisMetricsConfig  `yaml:"metrics"`
}

// Configuration is the top-level configuration struct.
type Configuration struct {
	Metadata map[string]string `yaml:"metadata"`
	Redis    RedisConfig       `yaml:"redis"`
}

// String returns a formatted string of the configuration.
func (c *Configuration) String() string {
	return fmt.Sprintf(`Configuration properties:
Metadata: %s
CollectionPeriodSeconds: %d
ClusterHealthProbePeriodSeconds: %d
ClusterHealingTimeSeconds: %d
RedisInfoKeys: %v`,
		util.MapToString(c.Metadata),
		c.Redis.Operator.CollectionPeriodSeconds,
		c.Redis.Cluster.HealthProbePeriodSeconds,
		c.Redis.Cluster.HealingTimeSeconds,
		c.Redis.Metrics.RedisInfoKeys)
}

// ConfigLoader defines the interface for loading a configuration.
type ConfigLoader interface {
	LoadConfig(path string) (*Configuration, error)
}

// YAMLConfigLoader implements ConfigLoader by reading YAML files.
type YAMLConfigLoader struct{}

// LoadConfig reads and decodes the YAML configuration from the specified file path.
func (y *YAMLConfigLoader) LoadConfig(path string) (*Configuration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read configuration file %s: %w", path, err)
	}
	var cfg Configuration
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration file %s: %w", path, err)
	}
	if missing := validateConfiguration(&cfg); len(missing) > 0 {
		return nil, fmt.Errorf("missing required configuration fields: %v", missing)
	}
	return &cfg, nil
}

// validateConfiguration checks for missing required configuration fields.
func validateConfiguration(cfg *Configuration) []string {
	var missing []string

	if len(cfg.Metadata) == 0 {
		missing = append(missing, "metadata")
	}
	if cfg.Redis.Operator.CollectionPeriodSeconds == 0 {
		missing = append(missing, "redis.operator.collection_interval_seconds")
	}
	if cfg.Redis.Cluster.Namespace == "" {
		missing = append(missing, "redis.cluster.namespace")
	}
	if cfg.Redis.Cluster.ServiceName == "" {
		missing = append(missing, "redis.cluster.service_name")
	}
	if cfg.Redis.Cluster.Replicas == 0 {
		missing = append(missing, "redis.cluster.replicas")
	}
	if cfg.Redis.Cluster.HealthProbePeriodSeconds == 0 {
		missing = append(missing, "redis.cluster.health_probe_interval_seconds")
	}
	if cfg.Redis.Cluster.HealingTimeSeconds == 0 {
		missing = append(missing, "redis.cluster.healing_time_seconds")
	}
	if cfg.Redis.Cluster.LabelSelector == "" {
		missing = append(missing, "redis.cluster.label_selector")
	}
	return missing
}

// GetConfiguration returns the singleton Configuration loaded from the default file.
// The configuration is loaded only once using sync.Once.
func GetConfiguration() *Configuration {
	var config *Configuration
	var err error
	var once sync.Once
	once.Do(func() {
		loader := &YAMLConfigLoader{}
		config, err = loader.LoadConfig("/opt/conf/configmap/application-configmap.yml")
		if err != nil {
			log.Fatalf("failed to load configuration: %v", err)
		}

	})
	return config
}
