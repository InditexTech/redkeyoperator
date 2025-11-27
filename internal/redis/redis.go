// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

const RedisCommPort = 6379
const RedisGossPort = 16379
const RedkeyClusterLabel = "redkey-cluster-name"
const RedkeyClusterComponentLabel = "redis.redkeycluster.operator/component"

var defaultPort = corev1.ServicePort{
	Name:     "client",
	Protocol: "TCP",
	Port:     RedisCommPort,
	TargetPort: intstr.IntOrString{
		Type:   0,
		IntVal: RedisCommPort,
	},
}

func CreateStatefulSet(ctx context.Context, req ctrl.Request, spec redkeyv1.RedkeyClusterSpec, labels map[string]string) (*v1.StatefulSet, error) {
	var err error = nil
	//	req ctrl.Request, replicas int32, redisImage string, storage string
	redisImage := spec.Image
	replicas := int32(spec.NodesNeeded())

	if redisImage == "" {
		redisImage = "redislabs/redisgraph:2.4.1"
	}

	defaultLabels := map[string]string{
		RedkeyClusterLabel:          req.Name,
		RedkeyClusterComponentLabel: common.ComponentLabelRedis,
	}

	// Add default labels and apply them to the statefulset.
	mergedLabels := labels
	for k, v := range defaultLabels {
		mergedLabels[k] = v
	}

	podManagementPolicy := v1.ParallelPodManagement
	redisStatefulSet := &v1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    mergedLabels,
		},
		Spec: v1.StatefulSetSpec{
			Replicas:            &replicas,
			PodManagementPolicy: podManagementPolicy,
			Selector: &metav1.LabelSelector{
				MatchLabels: defaultLabels,
			},
			ServiceName: req.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{RedkeyClusterLabel: req.Name, RedkeyClusterComponentLabel: common.ComponentLabelRedis},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: redisImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "client",
									ContainerPort: RedisCommPort,
								},
								{
									Name:          "gossip",
									ContainerPort: RedisGossPort,
								},
							},
							Command:        []string{"redis-server", "/conf/redis.conf"},
							LivenessProbe:  CreateProbe(15, 5),
							ReadinessProbe: CreateProbe(10, 5),
							Resources: corev1.ResourceRequirements{
								Limits:   corev1.ResourceList{},
								Requests: corev1.ResourceList{},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/conf",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: req.Name},
									Items:                []corev1.KeyToPath{{Key: "redis.conf", Path: "redis.conf"}},
								},
							},
						},
					},
				},
			},
		},
	}

	if !spec.Ephemeral {
		err = AddStatefulSetStorage(redisStatefulSet, req, spec)
	}

	return redisStatefulSet, err
}

func ApplyStsOverride(sts, override *v1.StatefulSet) (*v1.StatefulSet, error) {
	// Set an empty selector if override does not set it. This is because a merge with nil removes the field in the merged result
	if override.Spec.Selector == nil {
		override.Spec.Selector = &metav1.LabelSelector{}
	}

	// Copy the original serviceName. This is because if not, merged.Spec.ServiceName will be "" (override content)
	override.Spec.ServiceName = sts.Spec.ServiceName

	// Copy the original creationTimestamp and status
	override.ObjectMeta.CreationTimestamp = sts.ObjectMeta.CreationTimestamp
	override.Status = sts.Status

	// Marshal the original and the override to json
	original, err := json.Marshal(sts)
	if err != nil {
		return nil, err
	}

	patch, err := json.Marshal(override)
	if err != nil {
		return nil, err
	}

	// Do the strategic merge patch
	merged, err := strategicpatch.StrategicMergePatch(original, patch, v1.StatefulSet{})
	if err != nil {
		return nil, err
	}

	// Unmarshal the result to a StatefulSet
	var result *v1.StatefulSet
	err = json.Unmarshal(merged, &result)
	if err != nil {
		return nil, err
	}

	// Clean result
	cleanStatefulSetResult(result, sts, override)

	return result, nil
}

func cleanStatefulSetResult(result, original, override *v1.StatefulSet) {
	// Include the original redis container if the override does not set it. This can happen when the override containers is empty.
	// This could also be handled before the strategic merge patch, initializing the override containers with the original one or an empty array if it is empty.
	// However, that way the container deletion is not correctly handled, keeping the original container if the override container is empty.
	if result.Spec.Template.Spec.Containers == nil {
		result.Spec.Template.Spec.Containers = []corev1.Container{original.Spec.Template.Spec.Containers[0]}
	}

	// Assure redis container is in the first position
	if result.Spec.Template.Spec.Containers[0].Name != "redis" {
		firstContainer := result.Spec.Template.Spec.Containers[0]

		for i, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "redis" {
				result.Spec.Template.Spec.Containers[0] = container
				result.Spec.Template.Spec.Containers[i] = firstContainer
				break
			}
		}
	}

	// Copy the original resources of each container. This is because if not, merged.Spec.Template.Spec.Containers[].Resources will be nil (override content)
	for i, container := range result.Spec.Template.Spec.Containers {
		if container.Resources.Requests == nil {
			result.Spec.Template.Spec.Containers[i].Resources.Requests = corev1.ResourceList{}

			if len(original.Spec.Template.Spec.Containers) > i {
				result.Spec.Template.Spec.Containers[i].Resources.Requests = original.Spec.Template.Spec.Containers[i].Resources.Requests
			}
		}
		if container.Resources.Limits == nil {
			result.Spec.Template.Spec.Containers[i].Resources.Limits = corev1.ResourceList{}

			if len(original.Spec.Template.Spec.Containers) > i {
				result.Spec.Template.Spec.Containers[i].Resources.Limits = original.Spec.Template.Spec.Containers[i].Resources.Limits
			}
		}
	}

	// Assure to clean volumes if override does not set them
	if len(override.Spec.Template.Spec.Volumes) == 0 {
		result.Spec.Template.Spec.Volumes = []corev1.Volume{}

		// Mantain the config and data volumes (if exists)
		for _, volume := range original.Spec.Template.Spec.Volumes {
			if volume.Name == "config" || volume.Name == "data" {
				result.Spec.Template.Spec.Volumes = append(result.Spec.Template.Spec.Volumes, volume)
			}
		}
	}

	// Assure to clean tolerations if override does not set them
	if len(override.Spec.Template.Spec.Tolerations) == 0 {
		result.Spec.Template.Spec.Tolerations = nil
	}

	// Assure to clean TopologySpreadConstraints if override does not set them
	if len(override.Spec.Template.Spec.TopologySpreadConstraints) == 0 {
		result.Spec.Template.Spec.TopologySpreadConstraints = nil
	}

	// Assure to clean Affinity if override does not set them
	if override.Spec.Template.Spec.Affinity == nil {
		result.Spec.Template.Spec.Affinity = nil
	}

	// Assure to clean InitContainers if override does not set them
	if len(override.Spec.Template.Spec.InitContainers) == 0 {
		result.Spec.Template.Spec.InitContainers = nil
	}
}

func AddStatefulSetStorage(statefulSet *v1.StatefulSet, req ctrl.Request, spec redkeyv1.RedkeyClusterSpec) error {
	storage := spec.Storage
	if storage == "" {
		return errors.New("non ephemeral cluster with no storage defined in spec.Storage")
	}

	accessModes := spec.AccessModes
	accessModesTypes := make([]corev1.PersistentVolumeAccessMode, 0, 3)
	if accessModes != nil {
		for _, volumeAccessMode := range accessModes {
			switch volumeAccessMode {
			case corev1.ReadOnlyMany:
				accessModesTypes = append(accessModesTypes, corev1.ReadOnlyMany)
			case corev1.ReadWriteMany:
				accessModesTypes = append(accessModesTypes, corev1.ReadWriteMany)
			default:
				accessModesTypes = append(accessModesTypes, corev1.ReadWriteOnce)
			}
		}
	} else {
		accessModesTypes = append(accessModesTypes, corev1.ReadWriteOnce)
	}

	statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "data",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModesTypes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storage),
				},
			},
		}},
	}
	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: "data",
			},
		},
	})

	statefulSet.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulSet.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      "data",
		MountPath: "/data",
	})

	storageClassName := spec.StorageClassName
	if storageClassName != "" {
		statefulSet.Spec.VolumeClaimTemplates[0].Spec.StorageClassName = &storageClassName
	}

	return nil
}

func CreateProbe(initial int32, period int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(RedisCommPort)},
		},
		InitialDelaySeconds: initial,
		PeriodSeconds:       period,
	}
}

func CreateService(Namespace, Name string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind: "Service", APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name,
			Namespace: Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				defaultPort,
			},
			Selector:  map[string]string{RedkeyClusterLabel: Name, RedkeyClusterComponentLabel: common.ComponentLabelRedis},
			ClusterIP: "None",
		},
	}
}

func ApplyServiceOverride(service, override *corev1.Service) (*corev1.Service, error) {
	// Assure to copy the original ClusterIP to not allow overriding it
	override.Spec.ClusterIP = service.Spec.ClusterIP

	// Marshal the original and the override to json
	original, err := json.Marshal(service)
	if err != nil {
		return nil, err
	}

	patch, err := json.Marshal(override)
	if err != nil {
		return nil, err
	}

	// Do the strategic merge patch
	merged, err := strategicpatch.StrategicMergePatch(original, patch, corev1.Service{})
	if err != nil {
		return nil, err
	}

	// Unmarshal the result to a Service
	var result *corev1.Service
	err = json.Unmarshal(merged, &result)
	if err != nil {
		return nil, err
	}

	// Clean result
	cleanServiceResult(result, service, override)

	return result, nil
}

func cleanServiceResult(result, original, override *corev1.Service) {
	// Assure to clean ports if override does not set them
	if len(override.Spec.Ports) == 0 {
		result.Spec.Ports = []corev1.ServicePort{}
	}

	// Assure redis port exists
	found := false
	for _, port := range result.Spec.Ports {
		if port.Port == RedisCommPort {
			found = true
			break
		}
	}

	if !found {
		result.Spec.Ports = append(result.Spec.Ports, defaultPort)
	}

	// Assure to clean selector if override does not set them
	if len(override.Spec.Selector) == 0 {
		result.Spec.Selector = map[string]string{RedkeyClusterLabel: original.Name, RedkeyClusterComponentLabel: common.ComponentLabelRedis}
	}
}

func ApplyPodTemplateSpecOverride(podTemplateSpec, override corev1.PodTemplateSpec) (*corev1.PodTemplateSpec, error) {
	// Marshal the original and the override to json
	original, err := json.Marshal(podTemplateSpec)
	if err != nil {
		return nil, err
	}

	patch, err := json.Marshal(override)
	if err != nil {
		return nil, err
	}

	// Do the strategic merge patch
	merged, err := strategicpatch.StrategicMergePatch(original, patch, corev1.PodTemplateSpec{})
	if err != nil {
		return nil, err
	}

	// Unmarshal the result to a PodTemplateSpec
	var result *corev1.PodTemplateSpec
	err = json.Unmarshal(merged, &result)
	if err != nil {
		return nil, err
	}

	// Clean result
	cleanPodTemplateSpecResult(result, &override)

	return result, nil
}

func cleanPodTemplateSpecResult(result, override *corev1.PodTemplateSpec) {
	// Assure to clean volumes if override does not set them
	if len(override.Spec.Volumes) == 0 {
		result.Spec.Volumes = nil
	}

	// Copy the default resources of each container. This is because if not, merged.Spec.Containers[].Resources will be nil (override content)
	for i, container := range result.Spec.Containers {
		if container.Resources.Requests == nil {
			result.Spec.Containers[i].Resources.Requests = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("20m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
		if container.Resources.Limits == nil {
			result.Spec.Containers[i].Resources.Limits = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
	}

	// Assure to clean tolerations if override does not set them
	if len(override.Spec.Tolerations) == 0 {
		result.Spec.Tolerations = nil
	}

	// Assure to clean TopologySpreadConstraints if override does not set them
	if len(override.Spec.TopologySpreadConstraints) == 0 {
		result.Spec.TopologySpreadConstraints = nil
	}

	// Assure to clean Affinity if override does not set them
	if override.Spec.Affinity == nil {
		result.Spec.Affinity = nil
	}

	// Assure to clean InitContainers if override does not set them
	if len(override.Spec.InitContainers) == 0 {
		result.Spec.InitContainers = nil
	}
}

func ConfigStringToMap(config string) map[string][]string {
	configMap := make(map[string][]string)
	lines := strings.Split(strings.TrimSpace(config), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		kv := strings.Fields(line)
		if len(kv) < 2 {
			continue
		}

		key := kv[0]
		value := strings.Join(kv[1:], " ")

		// Add the value to the slice associated with the key
		if !slices.Contains(configMap[key], value) {
			configMap[key] = append(configMap[key], value)
		} else {
			log.Printf("Duplicate configuration detected for key %s: %s", key, value)
		}
	}

	return configMap
}

func GenerateRedisConfig(redkeyCluster *redkeyv1.RedkeyCluster) string {
	// Assuming RedkeyClusterType is the type of redkeyCluster
	// and it has Spec with Config, Ephemeral, and ReplicasPerMaster fields
	// Convert string configuration to map
	newRedkeyClusterConf := ConfigStringToMap(redkeyCluster.Spec.Config)

	// Merge new configuration with the default
	redisConfMap := MergeWithDefaultConfig(newRedkeyClusterConf, redkeyCluster.Spec.Ephemeral, redkeyCluster.Spec.ReplicasPerMaster)

	// Convert the merged configuration map to a string
	redisConf := MapToConfigString(redisConfMap)

	return redisConf
}

func MapToConfigString(config map[string][]string) string {
	var bynline []string
	keys := make([]string, 0, len(config))

	// Collect keys
	for key := range config {
		keys = append(keys, key)
	}

	// Sort keys
	sort.Strings(keys)

	// Build configuration string for each sorted key
	for _, key := range keys {
		values := config[key]
		for _, value := range values {
			bynline = append(bynline, fmt.Sprintf("%s %s", key, value))
		}
	}

	return strings.Join(bynline, "\n")
}

func ExtractMaxMemory(desiredConfig map[string][]string) (int, error) {
	maxMemoryValues, ok := desiredConfig["maxmemory"]
	if !ok || len(maxMemoryValues) == 0 {
		return 0, errors.New("maxmemory config is missing or empty")
	}

	// Assuming that we only consider the last value if multiple are provided
	maxMemory := strings.ToLower(maxMemoryValues[len(maxMemoryValues)-1])
	maxMemoryInt, err := convertRedisMemToMbytes(maxMemory)
	if err != nil {
		return 0, err
	}

	return maxMemoryInt, nil
}

// Default configuration params for Master-Replica clusters.
func DefaultConfigMasterReplica() map[string]string {
	config := make(map[string]string)
	config["maxmemory"] = "1600mb"
	config["maxmemory-samples"] = "5"
	config["maxmemory-policy"] = "allkeys-lru"
	config["appendonly"] = "yes"
	config["protected-mode"] = "no"
	config["dir"] = "/data"
	config["cluster-enabled"] = "yes"
	config["cluster-require-full-coverage"] = "no"
	config["cluster-node-timeout"] = "15"
	config["cluster-config-file"] = "/data/nodes.conf"
	config["cluster-migration-barrier"] = "1"
	config["repl-ping-replica-period"] = "1"
	config["cluster-replica-validity-factor"] = "1"
	config["tcp-keepalive"] = "0"
	config["timeout"] = "300"
	return config
}

// Default configuration params for not Master-Replica clusters.
func DefaultConfigNotMasterReplica() map[string]string {
	config := make(map[string]string)
	config["maxmemory"] = "1600mb"
	config["maxmemory-samples"] = "5"
	config["maxmemory-policy"] = "allkeys-lru"
	config["appendonly"] = "yes"
	config["protected-mode"] = "no"
	config["dir"] = "/data"
	config["cluster-enabled"] = "yes"
	config["cluster-require-full-coverage"] = "no"
	config["cluster-node-timeout"] = "15000"
	config["cluster-config-file"] = "/data/nodes.conf"
	config["cluster-migration-barrier"] = "1"
	return config
}

// Ephemeral clusters configuration params for Master-Replica clusters.
func EphemeralConfigMasterReplica() map[string]string {
	config := make(map[string]string)
	config["maxmemory"] = "1600mb"
	config["maxmemory-samples"] = "5"
	config["maxmemory-policy"] = "allkeys-lru"
	config["appendonly"] = "yes"
	config["protected-mode"] = "no"
	config["dir"] = "/tmp"
	config["cluster-enabled"] = "yes"
	config["cluster-require-full-coverage"] = "no"
	config["cluster-node-timeout"] = "15"
	config["cluster-config-file"] = "/tmp/nodes.conf"
	config["cluster-migration-barrier"] = "1"
	config["repl-ping-replica-period"] = "1"
	config["cluster-replica-validity-factor"] = "1"
	config["tcp-keepalive"] = "0"
	config["timeout"] = "300"
	return config
}

// Ephemeral clusters configuration params for not Master-Replica clusters.
func EphemeralConfigNotMasterReplica() map[string]string {
	config := make(map[string]string)
	config["maxmemory"] = "1600mb"
	config["maxmemory-samples"] = "5"
	config["maxmemory-policy"] = "allkeys-lru"
	config["appendonly"] = "yes"
	config["protected-mode"] = "no"
	config["dir"] = "/tmp"
	config["cluster-enabled"] = "yes"
	config["cluster-require-full-coverage"] = "no"
	config["cluster-node-timeout"] = "15000"
	config["cluster-config-file"] = "/tmp/nodes.conf"
	config["cluster-migration-barrier"] = "1"
	return config
}

func GetDefaultConfiguration(ephemeral bool, replicasPerMaster int32) map[string]string {
	switch {
	case ephemeral && replicasPerMaster > 0:
		return EphemeralConfigMasterReplica()
	case ephemeral:
		return EphemeralConfigNotMasterReplica()
	case !ephemeral && replicasPerMaster > 0:
		return DefaultConfigMasterReplica()
	default:
		return DefaultConfigNotMasterReplica()
	}
}

func MergeWithDefaultConfig(newConfig map[string][]string, ephemeral bool, replicasPerMaster int32) map[string][]string {
	defaultConfig := GetDefaultConfiguration(ephemeral, replicasPerMaster)
	allowConfiguration := make(map[string][]string, len(defaultConfig))

	overrideNotAllowed := map[string]bool{
		"dir":                           true,
		"cluster-enabled":               true,
		"cluster-require-full-coverage": true,
		"cluster-node-timeout":          true,
		"cluster-config-file":           true,
	}

	// Merge values from newConfig, respecting the overrideNotAllowed rules
	for key, value := range newConfig {
		if overrideNotAllowed[key] {
			// If override not allowed, use default value
			allowConfiguration[key] = []string{defaultConfig[key]}
		} else {
			// If override is allowed, use value from newConfig
			allowConfiguration[key] = value
		}
	}

	// Add any default configurations not present in newConfig
	for key, value := range defaultConfig {
		if _, exists := allowConfiguration[key]; !exists {
			allowConfiguration[key] = []string{value}
		}
	}

	return allowConfiguration
}

func convertRedisMemToMbytes(maxMemory string) (int, error) {
	maxMemory = strings.ToLower(maxMemory)
	var maxMemoryInt int
	var err error
	if strings.Contains(maxMemory, "kb") || strings.Contains(maxMemory, "k") {
		maxMemory = strings.Replace(maxMemory, "kb", "", 1)
		maxMemory = strings.Replace(maxMemory, "k", "", 1)
		maxMemoryInt, err = strconv.Atoi(maxMemory)
		maxMemoryInt = maxMemoryInt / 1024

	} else if strings.Contains(maxMemory, "mb") || strings.Contains(maxMemory, "m") {
		maxMemory = strings.Replace(maxMemory, "mb", "", 1)
		maxMemory = strings.Replace(maxMemory, "m", "", 1)
		maxMemoryInt, err = strconv.Atoi(maxMemory)
	} else if strings.Contains(maxMemory, "gb") || strings.Contains(maxMemory, "g") {
		maxMemory = strings.Replace(maxMemory, "gb", "", 1)
		maxMemory = strings.Replace(maxMemory, "g", "", 1)
		maxMemoryInt, err = strconv.Atoi(maxMemory)
		maxMemoryInt = maxMemoryInt * 1024
	} else {
		maxMemoryInt, err = strconv.Atoi(maxMemory)
		maxMemoryInt = maxMemoryInt / 1024 / 1024
	}
	return maxMemoryInt, err
}
