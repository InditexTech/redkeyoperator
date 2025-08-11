// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"testing"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/redis"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

var rc = newRedKeyCluster()
var recorder = newRecorder()
var reconciler = newReconciler(rc, recorder)
var configMap = newConfigMap()

func TestUpdateScalingStatus_ScalingDown(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas + 1

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.updateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusScalingDown)
	assertConditionTrue(t, rc, redkeyv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas - 1

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.updateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusScalingUp)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingDown)
	assertConditionTrue(t, rc, redkeyv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingDown_Completed(t *testing.T) {
	rc.Status.Status = redkeyv1.StatusScalingDown
	rc.Status.Conditions = []metav1.Condition{redkeyv1.ConditionScalingDown}

	numStatefulSetReplicas := rc.Spec.Replicas

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.updateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusReady)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp_Completed(t *testing.T) {
	rc.Status.Status = redkeyv1.StatusScalingUp
	rc.Status.Conditions = []metav1.Condition{redkeyv1.ConditionScalingUp}

	numStatefulSetReplicas := rc.Spec.Replicas

	readyNodes := newReadyNodes(int(numStatefulSetReplicas))

	rc.Status.Nodes = readyNodes

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	_ = reconciler.updateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusReady)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp_In_Progress(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas - 1

	readyNodes := newReadyNodes(int(numStatefulSetReplicas))

	rc.Status.Status = redkeyv1.StatusScalingUp
	rc.Status.Nodes = readyNodes

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	_ = reconciler.updateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusScalingUp)
	assertConditionFalse(t, rc, redkeyv1.ConditionScalingDown)
	assertConditionTrue(t, rc, redkeyv1.ConditionScalingUp)
}

func TestUpdateUpgradingStatus_Upgrading_Config_Not_Changed(t *testing.T) {
	rc.Status.Status = redkeyv1.StatusReady
	rc.Status.Conditions = []metav1.Condition{}

	// TODO: pass redis to configmap so it has same config
	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	numStatefulSetReplicas := rc.Spec.Replicas

	sset := newStatefulSet(rc, numStatefulSetReplicas)

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusReady)
	assertConditionFalse(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Completed(t *testing.T) {
	rc.Status.Status = redkeyv1.StatusUpgrading
	rc.Status.Conditions = []metav1.Condition{redkeyv1.ConditionUpgrading}

	// TODO: pass redis to configmap so it has same config
	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, rc.Spec.Replicas))
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusReady)
	assertConditionFalse(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Config_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	redisConfig := redis.ConfigStringToMap(rc.Spec.Config)

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.MapToConfigString(redisConfig)

	redisConfig["maxmemory"][0] = "1700"

	rc.Spec.Config = redis.MapToConfigString(redisConfig)

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Limits_Cpu_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	rc.Spec.Config = redis.GenerateRedisConfig(rc)

	sset := newStatefulSet(rc, numStatefulSetReplicas)
	limits := sset.Spec.Template.Spec.Containers[0].Resources.Limits
	limits[corev1.ResourceCPU] = resource.MustParse("10")

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Requests_Cpu_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	rc.Spec.Config = redis.GenerateRedisConfig(rc)

	sset := newStatefulSet(rc, numStatefulSetReplicas)
	requests := sset.Spec.Template.Spec.Containers[0].Resources.Requests
	requests[corev1.ResourceCPU] = resource.MustParse("10")

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Limits_Memory_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	rc.Spec.Config = redis.GenerateRedisConfig(rc)

	sset := newStatefulSet(rc, numStatefulSetReplicas)
	limits := sset.Spec.Template.Spec.Containers[0].Resources.Limits
	limits[corev1.ResourceMemory] = resource.MustParse("32Gi")

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Requests_Memory_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	rc.Spec.Config = redis.GenerateRedisConfig(rc)

	sset := newStatefulSet(rc, numStatefulSetReplicas)
	requests := sset.Spec.Template.Spec.Containers[0].Resources.Requests
	requests[corev1.ResourceMemory] = resource.MustParse("32Gi")

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Image_Changed(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas

	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	rc.Spec.Config = redis.GenerateRedisConfig(rc)

	sset := newStatefulSet(rc, numStatefulSetReplicas)
	sset.Spec.Template.Spec.Containers[0].Image = "redis-operator:9.9.9"

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.updateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redkeyv1.StatusUpgrading)
	assertConditionTrue(t, rc, redkeyv1.ConditionUpgrading)
}

func TestSetConditionTrue(t *testing.T) {
	clearConditions()
	initEventRecorder()

	condition := redkeyv1.ConditionUpgrading

	reconciler.setConditionTrue(rc, condition, "upgrading")

	assert.True(t, meta.IsStatusConditionTrue(rc.Status.Conditions, condition.Type))
}

func TestSetConditionTrue_recordEvent(t *testing.T) {
	clearConditions()
	initEventRecorder()

	message := "Updating resource requests"

	reconciler.setConditionTrue(rc, redkeyv1.ConditionUpgrading, message)

	select {
	case event := <-recorder.Events:
		assert.Equal(t, "Normal RedKeyClusterUpgrading "+message, event)
	default:
		t.Fail()
		fmt.Println("expected event to be recorded because condition was changed")
	}
}

func clearConditions() {
	rc.Status.Conditions = []metav1.Condition{}
}

func TestSetConditionTrue_dontRecordEvent(t *testing.T) {
	initEventRecorder()

	condition := redkeyv1.ConditionUpgrading

	rc.Status.Conditions = []metav1.Condition{condition}

	reconciler.setConditionTrue(rc, condition, "Upgrading")

	select {
	case <-recorder.Events:
		fmt.Println("event should only be recorded on condition change")
		t.Fail()
	default:
		fmt.Println("as expected no event was recorded because condition was not changed")
	}
}

func initEventRecorder() {
	recorder.Events = make(chan string, 1)
}

func newRecorder() *record.FakeRecorder {
	return &record.FakeRecorder{}
}
