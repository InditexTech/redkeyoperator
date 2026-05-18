// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRedkeyClusterSpec_NodesNeeded(t *testing.T) {
	tests := []struct {
		name               string
		primaries          int32
		replicasPerPrimary int32
		expected           int
	}{
		{"3 primaries, 0 replicas", 3, 0, 3},
		{"3 primaries, 1 replica", 3, 1, 6},
		{"3 primaries, 2 replicas", 3, 2, 9},
		{"1 primary, 0 replicas", 1, 0, 1},
		{"6 primaries, 1 replica", 6, 1, 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := RedkeyClusterSpec{
				Primaries:          tt.primaries,
				ReplicasPerPrimary: tt.replicasPerPrimary,
			}
			assert.Equal(t, tt.expected, spec.NodesNeeded())
		})
	}
}

func TestRedkeyCluster_NamespacedName(t *testing.T) {
	cluster := RedkeyCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cluster",
			Namespace: "production",
		},
	}

	nn := cluster.NamespacedName()
	assert.Equal(t, "my-cluster", nn.Name)
	assert.Equal(t, "production", nn.Namespace)
}

func TestRedkeyCluster_GetLabels_Nil(t *testing.T) {
	cluster := RedkeyCluster{
		Spec: RedkeyClusterSpec{
			Labels: nil,
		},
	}

	labels := cluster.GetLabels()
	assert.NotNil(t, labels)
	assert.Empty(t, labels)
}

func TestRedkeyCluster_GetLabels_WithValues(t *testing.T) {
	lbl := map[string]string{"env": "prod", "team": "platform"}
	cluster := RedkeyCluster{
		Spec: RedkeyClusterSpec{
			Labels: &lbl,
		},
	}

	labels := cluster.GetLabels()
	assert.Equal(t, "prod", labels["env"])
	assert.Equal(t, "platform", labels["team"])
}

func TestRedkeyClusterStatus_JSONIncludesStringPhase(t *testing.T) {
	status := RedkeyClusterStatus{
		Phase:  PhaseConfiguring,
		Status: ClusterStatusScalingUp,
		Substatus: RedkeyClusterSubstatus{
			Status:             "Rebalancing",
			UpgradingPartition: 1,
		},
		Nodes: map[string]*RedisNode{
			"redis-0": {
				Role:              "master",
				IP:                "10.0.0.1",
				ReplicationStatus: "synced",
			},
		},
	}

	payload, err := json.Marshal(status)
	assert.NoError(t, err)
	assert.JSONEq(t, `{"phase":"Configuring","status":"ScalingUp","substatus":{"status":"Rebalancing","upgradingPartition":1},"nodes":{"redis-0":{"role":"master","ip":"10.0.0.1","replicationStatus":"synced"}}}`, string(payload))
}

func TestRedkeyClusterConfigStatus_JSONIncludesEmptyStatusAndSubstatus(t *testing.T) {
	status := RedkeyClusterConfigStatus{
		ConfigPhase:        ConfigPhasePending,
		Nodes:              map[string]*RedisNode{},
		ObservedGeneration: 1,
	}

	payload, err := json.Marshal(status)
	assert.NoError(t, err)
	assert.JSONEq(t, `{"configPhase":"Pending","status":"","substatus":{"status":"","upgradingPartition":0},"nodes":{},"observedGeneration":1}`, string(payload))
}
