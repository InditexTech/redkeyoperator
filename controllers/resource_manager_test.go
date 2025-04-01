package controllers

import (
	"strconv"
	"testing"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	r "github.com/inditextech/redisoperator/internal/redis"
	"github.com/stretchr/testify/assert"
)

func TestCheckConfigurationStatus_Zero_Slots(t *testing.T) {
	rc.Spec.Replicas = 3

	clusterInfo := make(map[string]string)
	clusterInfo["cluster_slots_ok"] = "0"

	readyNodes := newReadyNodes(2)

	reconciler.GetClusterInfoFunc = mockClusterInfo(clusterInfo)
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	reconciler.CheckConfigurationStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusInitializing)
}

func TestCheckConfigurationStatus_Not_Ready_Nodes_Empty_Slots(t *testing.T) {
	rc.Spec.Replicas = 3

	clusterInfo := make(map[string]string)
	clusterInfo["cluster_slots_ok"] = ""

	readyNodes := newReadyNodes(2)

	reconciler.GetClusterInfoFunc = mockClusterInfo(clusterInfo)
	reconciler.GetReadyNodesFunc = mockReadyNodes(readyNodes)

	reconciler.CheckConfigurationStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusInitializing)
}

func TestCheckConfigurationStatus_Unassigned_Slots(t *testing.T) {
	clusterInfo := make(map[string]string)
	clusterInfo["cluster_slots_assigned"] = "1000"
	clusterInfo["cluster_slots_ok"] = "500"

	reconciler.GetClusterInfoFunc = mockClusterInfo(clusterInfo)

	reconciler.CheckConfigurationStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusConfiguring)
}

func TestCheckConfigurationStatus_Ready_Nodes_Empty_Slots(t *testing.T) {
	rc.Spec.Replicas = 3

	clusterInfo := make(map[string]string)
	clusterInfo["cluster_slots_ok"] = ""

	reconciler.GetClusterInfoFunc = mockClusterInfo(clusterInfo)
	reconciler.GetReadyNodesFunc = mockReadyNodes(newReadyNodes(3))

	reconciler.CheckConfigurationStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusConfiguring)
}

func TestCheckConfigurationStatus_All_Slot_Assigned(t *testing.T) {
	rc.Status.Status = redisv1.StatusConfiguring

	totalClusterSlots := strconv.Itoa(r.TotalClusterSlots)

	clusterInfo := make(map[string]string)
	clusterInfo["cluster_state"] = "ok"
	clusterInfo["cluster_slots_ok"] = totalClusterSlots
	clusterInfo["cluster_slots_assigned"] = totalClusterSlots

	reconciler.GetClusterInfoFunc = mockClusterInfo(clusterInfo)

	reconciler.CheckConfigurationStatus(newContext(), rc)

	assertStatusEqual(t, rc, redisv1.StatusReady)
}

func TestCheckAndUpdateRDCL(t *testing.T) {
	reconciler.CheckAndUpdateRDCL(newContext(), "rediscluster-test", rc)
	redisclusterName := rc.ObjectMeta.Labels[r.RedisClusterLabel]
	redisClusterType := rc.ObjectMeta.Labels[r.RedisClusterComponentLabel]

	assert.NotNil(t, redisclusterName)
	assert.NotNil(t, redisClusterType)
	assert.Equal(t, "rediscluster-test", redisclusterName)
	assert.Equal(t, "redis", redisClusterType)
}
