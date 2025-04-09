// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"reflect"
	"testing"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/redis"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

var rc = newRedisCluster()
var recorder = newRecorder()
var reconciler = newReconciler(rc, recorder)
var configMap = newConfigMap()
var node1 *kubernetes.ClusterNode = &kubernetes.ClusterNode{ClusterNode: redis.NewTestClusterNode("node-1")}
var node2 *kubernetes.ClusterNode = &kubernetes.ClusterNode{ClusterNode: redis.NewTestClusterNode("node-2")}
var node3 *kubernetes.ClusterNode = &kubernetes.ClusterNode{ClusterNode: redis.NewTestClusterNode("node-3")}

func TestUpdateScalingStatus_ScalingDown(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas + 1

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.UpdateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusScalingDown)
	assertConditionTrue(t, rc, redisv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redisv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas - 1

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.UpdateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusScalingUp)
	assertConditionFalse(t, rc, redisv1.ConditionScalingDown)
	assertConditionTrue(t, rc, redisv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingDown_Completed(t *testing.T) {
	rc.Status.Status = redisv1.StatusScalingDown
	rc.Status.Conditions = []metav1.Condition{redisv1.ConditionScalingDown}

	numStatefulSetReplicas := rc.Spec.Replicas

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))

	_ = reconciler.UpdateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusReady)
	assertConditionFalse(t, rc, redisv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redisv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp_Completed(t *testing.T) {
	rc.Status.Status = redisv1.StatusScalingUp
	rc.Status.Conditions = []metav1.Condition{redisv1.ConditionScalingUp}

	numStatefulSetReplicas := rc.Spec.Replicas

	readyNodes := newReadyNodes(int(numStatefulSetReplicas))

	rc.Status.Nodes = readyNodes

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	_ = reconciler.UpdateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusReady)
	assertConditionFalse(t, rc, redisv1.ConditionScalingDown)
	assertConditionFalse(t, rc, redisv1.ConditionScalingUp)
}

func TestUpdateScalingStatus_ScalingUp_In_Progress(t *testing.T) {
	numStatefulSetReplicas := rc.Spec.Replicas - 1

	readyNodes := newReadyNodes(int(numStatefulSetReplicas))

	rc.Status.Status = redisv1.StatusScalingUp
	rc.Status.Nodes = readyNodes

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, numStatefulSetReplicas))
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	_ = reconciler.UpdateScalingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusScalingUp)
	assertConditionFalse(t, rc, redisv1.ConditionScalingDown)
	assertConditionTrue(t, rc, redisv1.ConditionScalingUp)
}

func TestUpdateUpgradingStatus_Upgrading_Config_Not_Changed(t *testing.T) {
	rc.Status.Status = redisv1.StatusReady
	rc.Status.Conditions = []metav1.Condition{}

	// TODO: pass redis to configmap so it has same config
	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	numStatefulSetReplicas := rc.Spec.Replicas

	sset := newStatefulSet(rc, numStatefulSetReplicas)

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(sset)
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusReady)
	assertConditionFalse(t, rc, redisv1.ConditionUpgrading)
}

func TestUpdateUpgradingStatus_Upgrading_Completed(t *testing.T) {
	rc.Status.Status = redisv1.StatusUpgrading
	rc.Status.Conditions = []metav1.Condition{redisv1.ConditionUpgrading}

	// TODO: pass redis to configmap so it has same config
	configMap.Data = make(map[string]string)
	configMap.Data["redis.conf"] = redis.GenerateRedisConfig(rc)

	reconciler.FindExistingStatefulSetFunc = mockStatefulSet(newStatefulSet(rc, rc.Spec.Replicas))
	reconciler.FindExistingConfigMapFunc = mockConfigMap(configMap)

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusReady)
	assertConditionFalse(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
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

	_ = reconciler.UpdateUpgradingStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusUpgrading)
	assertConditionTrue(t, rc, redisv1.ConditionUpgrading)
}

func TestSetConditionTrue(t *testing.T) {
	clearConditions()
	initEventRecorder()

	condition := redisv1.ConditionUpgrading

	reconciler.SetConditionTrue(rc, condition, "upgrading")

	assert.True(t, meta.IsStatusConditionTrue(rc.Status.Conditions, condition.Type))
}

func TestSetConditionTrue_recordEvent(t *testing.T) {
	clearConditions()
	initEventRecorder()

	message := "Updating resource requests"

	reconciler.SetConditionTrue(rc, redisv1.ConditionUpgrading, message)

	select {
	case event := <-recorder.Events:
		assert.Equal(t, "Normal RedisClusterUpgrading "+message, event)
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

	condition := redisv1.ConditionUpgrading

	rc.Status.Conditions = []metav1.Condition{condition}

	reconciler.SetConditionTrue(rc, condition, "Upgrading")

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

func TestCalculateSlotMoveSequence(t *testing.T) {
	testTable := map[string]struct {
		startingMap          map[*kubernetes.ClusterNode][]int
		expectedMoveSequence []kubernetes.MoveSequence
	}{
		"simple-move-test": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 10),
				node2: makeRange(11, 21),
				node3: makeRange(22, 23),
			},
			[]kubernetes.MoveSequence{
				{
					From:     "node-1",
					FromNode: node1,
					To:       "node-3",
					ToNode:   node3,
					Slots:    []int{8, 9, 10},
				},
				{
					From:     "node-2",
					FromNode: node2,
					To:       "node-3",
					ToNode:   node3,
					Slots:    []int{19, 20, 21},
				},
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		slotMoveMap, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			t.Errorf("Error calculating SlotMoveMap: %s", err)
			break
		}
		got := kubernetes.CalculateMoveSequence(table.startingMap, slotMoveMap, options)

		if len(got) != len(table.expectedMoveSequence) {
			t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveSequence)
			t.Errorf("Another error")
		}

		for _, gotNode := range got {
			found := false
			for _, expectedNode := range table.expectedMoveSequence {
				if gotNode.From == expectedNode.From && gotNode.To == expectedNode.To {
					found = reflect.DeepEqual(gotNode, expectedNode)
					break
				}
			}
			if !found {
				t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, gotNode, table.expectedMoveSequence)
			}
		}
	}
}

func TestCalculateSlotMoveSequenceWithWeights(t *testing.T) {
	testTable := map[string]struct {
		startingMap          map[*kubernetes.ClusterNode][]int
		expectedMoveSequence []kubernetes.MoveSequence
		weights              map[string]int
	}{
		"without-weights": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 10),
				node2: makeRange(11, 21),
				node3: makeRange(22, 23),
			},
			[]kubernetes.MoveSequence{
				{
					From:     "node-1",
					FromNode: node1,
					To:       "node-3",
					ToNode:   node3,
					Slots:    []int{8, 9, 10},
				},
				{
					From:     "node-2",
					FromNode: node2,
					To:       "node-3",
					ToNode:   node3,
					Slots:    []int{19, 20, 21},
				},
			},
			map[string]int{},
		},
		"with-two-0-weights": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 10),
				node2: makeRange(11, 21),
				node3: makeRange(22, 23),
			},
			[]kubernetes.MoveSequence{
				{
					From:     "node-2",
					FromNode: node2,
					To:       "node-1",
					ToNode:   node1,
					Slots:    makeRange(11, 21),
				},
				{
					From:     "node-3",
					FromNode: node3,
					To:       "node-1",
					ToNode:   node1,
					Slots:    makeRange(22, 23),
				},
			},
			map[string]int{
				"node-2": 0,
				"node-3": 0,
			},
		},
		"with-one-2-weight": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 10),
				node2: makeRange(11, 21),
				node3: makeRange(22, 23),
			},
			[]kubernetes.MoveSequence{
				{
					From:     "node-1",
					FromNode: node1,
					To:       "node-3",
					ToNode:   node3,
					Slots:    makeRange(6, 10),
				},
				{
					From:     "node-2",
					FromNode: node2,
					To:       "node-3",
					ToNode:   node3,
					Slots:    makeRange(17, 21),
				},
			},
			map[string]int{
				"node-3": 2,
			},
		},
		"with-1-slot-versus-16386-and-0-weight": {
			// To to rounding errors, we could end up with a 0 weighted node, which still has slots after calculation.
			// This test protects that from happening
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(1, 8192),
				node2: makeRange(8193, 16383),
				node3: makeRange(16384, 16384),
			},
			[]kubernetes.MoveSequence{
				{
					From:     "node-3",
					FromNode: node3,
					To:       "node-2",
					ToNode:   node2,
					Slots:    makeRange(16384, 16384),
				},
			},
			map[string]int{
				"node-3": 0,
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		options.SetWeights(table.weights)
		slotMoveMap, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			t.Errorf("Error calculating SlotMoveMap: %s", err)
			break
		}
		got := kubernetes.CalculateMoveSequence(table.startingMap, slotMoveMap, options)

		if len(got) != len(table.expectedMoveSequence) {
			t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveSequence)
			t.Errorf("Another error")
		}

		for _, gotNode := range got {
			found := false
			for _, expectedNode := range table.expectedMoveSequence {
				if gotNode.From == expectedNode.From && gotNode.To == expectedNode.To {
					found = reflect.DeepEqual(gotNode, expectedNode)
					break
				}
			}
			if !found {
				t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveSequence)
			}
		}
	}
}

func TestCalculateSlotMoveSequenceWithWeightsMultiple(t *testing.T) {
	testTable := map[string]struct {
		startingMap                       map[*kubernetes.ClusterNode][]int
		expectedMoveSequencePossibilities [][]kubernetes.MoveSequence
		weights                           map[string]int
	}{
		"with-uneven-numbers-and-0-weight": {
			// To to rounding errors, we could end up with a 0 weighted node, which still has slots after calculation.
			// This test protects that from happening
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(1, 10),
				node2: makeRange(11, 20),
				node3: makeRange(21, 27),
			},
			[][]kubernetes.MoveSequence{
				{
					{
						From:     "node-3",
						FromNode: node3,
						To:       "node-1",
						ToNode:   node1,
						Slots:    makeRange(21, 24),
					},
					{
						From:     "node-3",
						FromNode: node3,
						To:       "node-2",
						ToNode:   node2,
						Slots:    makeRange(25, 27),
					},
				},
			},
			map[string]int{
				"node-3": 0,
			},
		},
		"with-one-0-weight": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 10),
				node2: makeRange(11, 21),
				node3: makeRange(22, 23),
			},
			[][]kubernetes.MoveSequence{
				{
					{
						From:     "node-3",
						FromNode: node3,
						To:       "node-1",
						ToNode:   node1,
						Slots:    []int{22},
					},
					{
						From:     "node-3",
						FromNode: node3,
						To:       "node-2",
						ToNode:   node2,
						Slots:    []int{23},
					},
				},
			},
			map[string]int{
				"node-3": 0,
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		options.SetWeights(table.weights)
		slotMoveMap, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			t.Errorf("Error calculating SlotMoveMap: %s", err)
			break
		}
		got := kubernetes.CalculateMoveSequence(table.startingMap, slotMoveMap, options)

		for _, gotNode := range got {
			found := false
			for _, possibility := range table.expectedMoveSequencePossibilities {
				if found == false {
					for _, expectedNode := range possibility {
						if gotNode.From == expectedNode.From && gotNode.To == expectedNode.To {
							fmt.Println(gotNode, expectedNode)
							found = reflect.DeepEqual(gotNode, expectedNode)
							break
						}
					}
				}
			}
			if !found {
				t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected Possibilities %v.", name, got, table.expectedMoveSequencePossibilities)
			}
		}
	}
}

func TestSlotMoveMap(t *testing.T) {
	var node1 *kubernetes.ClusterNode = &kubernetes.ClusterNode{
		ClusterNode: redis.NewTestClusterNode("node-1"),
	}
	var node2 *kubernetes.ClusterNode = &kubernetes.ClusterNode{
		ClusterNode: redis.NewTestClusterNode("node-2"),
	}
	var node3 *kubernetes.ClusterNode = &kubernetes.ClusterNode{
		ClusterNode: redis.NewTestClusterNode("node-3"),
	}

	testTable := map[string]struct {
		startingMap     map[*kubernetes.ClusterNode][]int
		expectedMoveMap map[string][]int
	}{
		"simple-move-test": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 16200),
				node3: makeRange(16201, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": makeRange(10924, 16200),
				"node-3": []int{},
			},
		},
		"empty-node-move-map": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 16384),
				node2: []int{},
				node3: []int{},
			},
			map[string][]int{
				"node-1": makeRange(5462, 16384),
				"node-2": []int{},
				"node-3": []int{},
			},
		},
		"balanced-node-move-map": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 10922),
				node3: makeRange(10923, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": []int{},
				"node-3": []int{},
			},
		},
		"balanced-node-move-map-within-threshold": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5440),
				node2: makeRange(5441, 10922),
				node3: makeRange(10923, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": []int{},
				"node-3": []int{},
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		options.SetThreshold(2)
		got, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			t.Errorf("Error calculating SlotMoveMap: %s", err)
			break
		}

		if !reflect.DeepEqual(getNamedResults(got), table.expectedMoveMap) {
			t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveMap)
		}
	}
}

func TestSlotMoveMapWithWeights(t *testing.T) {
	testTable := map[string]struct {
		startingMap     map[*kubernetes.ClusterNode][]int
		expectedMoveMap map[string][]int
		weights         map[string]int
	}{
		"simple-move-test-without-weights": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 16200),
				node3: makeRange(16201, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": makeRange(10924, 16200),
				"node-3": []int{},
			},
			map[string]int{},
		},
		"simple-move-test-with-one-0-weight": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 10922),
				node3: makeRange(10923, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": []int{},
				"node-3": makeRange(10923, 16384),
			},
			map[string]int{
				"node-3": 0,
			},
		},
		"simple-move-test-with-two-0-weights": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 16200),
				node3: makeRange(16201, 16384),
			},
			map[string][]int{
				"node-1": []int{},
				"node-2": makeRange(5462, 16200),
				"node-3": makeRange(16201, 16384),
			},
			map[string]int{
				"node-2": 0,
				"node-3": 0,
			},
		},
		"simple-move-test-with-one-2-weights": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 10922),
				node3: makeRange(10923, 16384),
			},
			map[string][]int{
				"node-1": makeRange(4097, 5461),
				"node-2": makeRange(9559, 10922),
				"node-3": []int{},
			},
			map[string]int{
				"node-3": 2,
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		options.SetThreshold(2)
		options.SetWeights(table.weights)
		got, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			t.Errorf("Error calculating SlotMoveMap: %s", err)
			break
		}

		if !reflect.DeepEqual(getNamedResults(got), table.expectedMoveMap) {
			t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveMap)
		}
	}
}

func TestSlotMoveMapWithWeightsToZero(t *testing.T) {
	testTable := map[string]struct {
		startingMap     map[*kubernetes.ClusterNode][]int
		expectedMoveMap map[string][]int
		weights         map[string]int
	}{
		"simple-move-test-with-weights-to-zero": {
			map[*kubernetes.ClusterNode][]int{
				node1: makeRange(0, 5461),
				node2: makeRange(5462, 10922),
				node3: makeRange(10923, 16384),
			},
			map[string][]int{
				"node-1": makeRange(4096, 5461),
				"node-2": makeRange(9558, 10922),
				"node-3": []int{},
			},
			map[string]int{
				"node-1": 0,
				"node-2": 0,
				"node-3": 0,
			},
		},
	}

	for name, table := range testTable {
		options := kubernetes.NewMoveMapOptions()
		options.SetThreshold(2)
		options.SetWeights(table.weights)
		got, err := kubernetes.CalculateSlotMoveMap(table.startingMap, options)
		if err != nil {
			fmt.Println("Error detected as expected from CalculateSlotMoveMap")
			break
		}

		if !reflect.DeepEqual(getNamedResults(got), table.expectedMoveMap) {
			t.Errorf("Unexpected slot map received from %s iteration. Got %v :: Expected %v.", name, got, table.expectedMoveMap)
		}
	}
}

func getNamedResults(results map[*kubernetes.ClusterNode][]int) map[string][]int {
	namedResults := make(map[string][]int)
	for k, v := range results {
		namedResults[k.Name()] = v
	}
	return namedResults
}
