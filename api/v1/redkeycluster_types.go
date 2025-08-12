// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// StatusUpgrading: A RedKeyCluster enters this status when when:
//   - there are differences between the existing configuration in the configmap
//     and the configuration of the RedKeyCluster object merged with the default configuration set in the code.
//   - there is a mismatch between the StatefulSet object labels and the RedKeyCluster Spec labels.
//   - a mismatch exists between RedKeyCluster resources defined under spec and effective resources defined in the StatefulSet.
//   - the images set in RedKeyCluster under spec and the image set in the StatefulSet object are not the same.
//     The cluster is upgraded, reconfiguring the objects to solve these mismatches.
//
// StatusScalingDown: RedKeyCluster replicas > StatefulSet replicas
//
//	The cluster enters in this status to remove excess nodes.
//
// StatusScalingUp: RedKeyCluster replicas < StatefulSet replicas
//
//	The cluster enters in this status to create the needed nodes to equal the desired replicas with the current replicas.
//
// Ready: The cluster has the correct configuration, the desired number of replicas, is rebalances and ready to be used.
//
//	The operator checks pediodically if the cluster can be kept in this status.
//
// Configuring: Not all the cluster slots are OK but every cluster node are up and ready.
//
//	If the cluster needs a meet or a rebalance, being in Ready status, its status will switch to
//	Configuring.
//
// Initializing: Not all the cluster slots are OK and not all the cluster nodes are up and ready.
// Error: An error is detected in the cluster.
//   - Storage capacity mismatch.
//   - Storage class mismatch.
//   - Scaling up the cluster before upgrading raises an error.
//   - Scaling down the cluster after upgradind raises an error.
//   - Scaling up when in StatusScalingUp status goes wrong.
//   - Scaling down when in StatusScalingDown status goes wrong.
//     The operator tries to recover the cluster from error checking the configuration and/or scaling the cluster.
const (
	StatusUpgrading    = "Upgrading"
	StatusScalingDown  = "ScalingDown"
	StatusScalingUp    = "ScalingUp"
	StatusReady        = "Ready"
	StatusConfiguring  = "Configuring"
	StatusInitializing = "Initializing"
	StatusError        = "Error"

	SubstatusFastUpgrading        = "FastUpgrading"
	SubstatusEndingFastUpgrading  = "EndingFastUpgrading"
	SubstatusSlowUpgrading        = "SlowUpgrading"
	SubstatusUpgradingScalingUp   = "ScalingUp"
	SubstatusUpgradingScalingDown = "ScalingDown"
	SubstatusEndingSlowUpgrading  = "EndingSlowUpgrading"

	SubstatusFastScaling       = "FastScaling"
	SubstatusEndingFastScaling = "EndingFastScaling"
	SubstatusScalingPods       = "PodScaling"
	SubstatusScalingRobin      = "RobinScaling"
	SubstatusEndingScaling     = "EndingScaling"
)

var ConditionUpgrading = metav1.Condition{
	Type:               "Upgrading",
	LastTransitionTime: metav1.Now(),
	Message:            "RedKey cluster is upgrading",
	Reason:             "RedKeyClusterUpgrading",
	Status:             metav1.ConditionTrue,
}

var ConditionScalingUp = metav1.Condition{
	Type:               "ScalingUp",
	LastTransitionTime: metav1.Now(),
	Message:            "RedKey cluster is scaling up",
	Reason:             "RedKeyClusterScalingUp",
	Status:             metav1.ConditionTrue,
}
var ConditionScalingDown = metav1.Condition{
	Type:               "ScalingDown",
	LastTransitionTime: metav1.Now(),
	Message:            "RedKey cluster is scaling down",
	Reason:             "RedKeyClusterScalingDown",
	Status:             metav1.ConditionTrue,
}

var AllConditions = []metav1.Condition{ConditionUpgrading, ConditionScalingUp, ConditionScalingDown}

// RedKeyClusterSpec defines the desired state of RedKeyCluster
// +kubebuilder:validation:XValidation:rule="self.ephemeral || has(self.storage)", message="Ephemeral or storage must be set"
// +kubebuilder:validation:XValidation:rule="!(self.ephemeral && has(self.storage))", message="Ephemeral and storage cannot be combined"
type RedKeyClusterSpec struct {
	// +kubebuilder:validation:Optional
	// RedisAuth
	Auth RedisAuth `json:"auth,omitempty"`

	// +kubebuilder:validation:Optional
	// Redis version
	Version string `json:"version,omitempty"`

	// Replicas specifies the number of Redis nodes in the cluster.
	// +kubebuilder:validation:Required
	Replicas int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// ReplicasPerMaster specifies how many replicas should be attached to each Redis Master
	//+operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Number of replicas per Master Node"
	ReplicasPerMaster int32 `json:"replicasPerMaster,omitempty"`

	// +kubebuilder:validation:Optional
	// Image is the Redis image to use.
	Image string `json:"image,omitempty"`

	// +kubebuilder:validation:Optional
	// DeletePVC specifies if the PVC should be deleted when the RedKeyCluster is deleted.
	DeletePVC bool `json:"deletePVC,omitempty"`

	// +kubebuilder:validation:Optional
	// Backup specifies if the RedKeyCluster should be backed up.
	Backup bool `json:"backup,omitempty"`

	// +kubebuilder:validation:Optional
	// Robin specifies the robin configuration for the RedKeyCluster.
	Robin *RobinSpec `json:"robin,omitempty"`

	// +kubebuilder:validation:Optional
	// PurgeKeysOnRebalance specifies if keys should be purged on rebalance.
	PurgeKeysOnRebalance bool `json:"purgeKeysOnRebalance,omitempty"`

	// +kubebuilder:validation:Optional
	// Config is the Redis configuration to use.
	Config string `json:"config,omitempty"`

	// +kubebuilder:validation:Optional
	// Resources is the resource requirements for the RedKeyCluster.
	Resources *v1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:validation:Optional
	// Labels is the labels to add to the RedKeyCluster.
	Labels *map[string]string `json:"labels,omitempty"`

	// +kubebuilder:validation:Optional
	// Pdb is the PodDisruptionBudget configuration for the RedKeyCluster.
	Pdb Pdb `json:"pdb,omitempty"`

	// +kubebuilder:validation:Optional
	Override *RedKeyClusterOverrideSpec `json:"override,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Changing the ephemeral field is not allowed"
	// Ephemeral storage is not persisted across pod restarts.
	Ephemeral bool `json:"ephemeral"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Changing the storage size is not allowed"
	// Storage is the amount of persistent storage to request for each Redis node.
	Storage string `json:"storage,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Changing the storage class name is not allowed"
	// StorageClassName is the name of the StorageClass to use for the PVC.
	StorageClassName string `json:"storageClassName"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={ReadWriteOnce}
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Changing the storage access modes is not allowed"
	// +kubebuilder:validation:items:Enum={ReadWriteOnce,ReadOnlyMany,ReadWriteMany}
	// AccessModes is the list of access modes for the PVC.
	AccessModes []v1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

func (redkeyClusterSpec RedKeyClusterSpec) NodesNeeded() int {
	return int(redkeyClusterSpec.Replicas + (redkeyClusterSpec.Replicas * redkeyClusterSpec.ReplicasPerMaster))
}

// Provides the ability to override the generated manifest of several child resources.
type RedKeyClusterOverrideSpec struct {
	// +kubebuilder:validation:Optional
	// Override configuration for the RedKeyCluster StatefulSet.
	StatefulSet *appsv1.StatefulSet `json:"statefulSet,omitempty"`

	// +kubebuilder:validation:Optional
	// Override configuration for the RedKeyCluster Service.
	Service *v1.Service `json:"service,omitempty"`
}

type RobinSpec struct {
	Template *v1.PodTemplateSpec `json:"template,omitempty"`
	Config   *string             `json:"config,omitempty"`
}

// RedKeyClusterStatus defines the observed state of RedKeyCluster
type RedKeyClusterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make manifests" to regenerate code after modifying this file
	Nodes      map[string]*RedisNode `json:"nodes"`
	Status     string                `json:"status"`
	Conditions []metav1.Condition    `json:"conditions,omitempty"`
	Substatus  RedKeyClusterSubstatus `json:"substatus"`
}

type RedKeyClusterSubstatus struct {
	Status             string `json:"status,omitempty"`
	UpgradingPartition string `json:"upgradingPartition,omitempty"`
}

type SlotRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type RedisNode struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	IsMaster  bool   `json:"isMaster"`
	ReplicaOf string `json:"replicaOf"`
}

type RedisAuth struct {
	SecretName string `json:"secret,omitempty"`
}
type Pdb struct {
	Enabled            bool               `json:"enabled,omitempty"`
	PdbSizeUnavailable intstr.IntOrString `json:"pdbSizeUnavailable,omitempty"`
	PdbSizeAvailable   intstr.IntOrString `json:"pdbSizeAvailable,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rkcl
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Amount of Redis nodes"
// +kubebuilder:printcolumn:name="Image",type="string",JSONPath=".spec.image",description="Source image for Redis instance"
// +kubebuilder:printcolumn:name="Storage",type="string",JSONPath=".spec.storage",description="Amount of storage for Redis"
// +kubebuilder:printcolumn:name="StorageClassName",type="string",JSONPath=".spec.storageClassName",description="Storage Class to be used by the PVC"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="The status of Redis cluster"
// +kubebuilder:printcolumn:name="Substatus",type="string",JSONPath=".status.substatus.status",description="The substatus of Redis cluster"
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// RedKeyCluster is the Schema for the redkeyclusters API
type RedKeyCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RedKeyClusterSpec   `json:"spec,omitempty"`
	Status            RedKeyClusterStatus `json:"status,omitempty"`
}

func (redkeyCluster RedKeyCluster) NodesNeeded() int {
	return redkeyCluster.Spec.NodesNeeded()
}

//+kubebuilder:object:root=true

// RedKeyClusterList contains a list of RedKeyCluster
type RedKeyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RedKeyCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RedKeyCluster{}, &RedKeyClusterList{})
}

func (redkeyCluster RedKeyCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: redkeyCluster.GetNamespace(),
		Name:      redkeyCluster.GetName(),
	}
}

func CompareStatuses(a, b *RedKeyClusterStatus) bool {
	if a.Status != b.Status {
		return false
	}
	if a.Substatus.Status != b.Substatus.Status {
		return false
	}
	for _, nodeA := range a.Nodes {
		nodeB := RedisNode{}
		for _, node := range b.Nodes {
			if node.Name == nodeA.Name {
				nodeB = *node
				break
			}
		}
		if nodeA.Name != nodeB.Name || nodeA.IP != nodeB.IP || nodeA.IsMaster != nodeB.IsMaster || nodeA.ReplicaOf != nodeB.ReplicaOf {
			return false
		}
	}

	return true
}

func IsFastOperationStatus(status RedKeyClusterSubstatus) bool {
	return status.Status == SubstatusFastScaling || status.Status == SubstatusEndingFastScaling ||
		status.Status == SubstatusFastUpgrading || status.Status == SubstatusEndingFastUpgrading
}
