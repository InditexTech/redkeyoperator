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

// StatusUpgrading: A RedkeyCluster enters this status when when:
//   - there are differences between the existing configuration in the configmap
//     and the configuration of the RedkeyCluster object merged with the default configuration set in the code.
//   - there is a mismatch between the StatefulSet object labels and the RedkeyCluster Spec labels.
//   - a mismatch exists between RedkeyCluster resources defined under spec and effective resources defined in the StatefulSet.
//   - the images set in RedkeyCluster under spec and the image set in the StatefulSet object are not the same.
//     The cluster is upgraded, reconfiguring the objects to solve these mismatches.
//
// StatusScalingDown: RedkeyCluster primaries > StatefulSet replicas
//
//	The cluster enters in this status to remove excess nodes.
//
// StatusScalingUp: RedkeyCluster primaries < StatefulSet replicas
//
//	The cluster enters in this status to create the needed nodes to equal the desired primary nodes count with the current primary nodes count.
//
// Ready: The cluster has the correct configuration, the desired number of primary nodes, is rebalances and ready to be used.
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
//
// Maintenance: The cluster is under maintenance mode, the operator won't perform any operations on it. Robin will be updated with this status too.
const (
	StatusUpgrading    = "Upgrading"
	StatusScalingDown  = "ScalingDown"
	StatusScalingUp    = "ScalingUp"
	StatusReady        = "Ready"
	StatusConfiguring  = "Configuring"
	StatusInitializing = "Initializing"
	StatusError        = "Error"
	StatusMaintenance  = "Maintenance"

	RobinStatusNoReconciling = "NoReconciling"
	RobinStatusUpgrading     = "Upgrading"
	RobinStatusScalingDown   = "ScalingDown"
	RobinStatusScalingUp     = "ScalingUp"
	RobinStatusReady         = "Ready"
	RobinStatusConfiguring   = "Configuring"
	RobinStatusInitializing  = "Initializing"
	RobinStatusError         = "Error"
	RobinStatusMaintenance   = "Maintenance"

	SubstatusFastUpgrading        = "FastUpgrading"
	SubstatusEndingFastUpgrading  = "EndingFastUpgrading"
	SubstatusSlowUpgrading        = "SlowUpgrading"
	SubstatusUpgradingScalingUp   = "ScalingUp"
	SubstatusUpgradingScalingDown = "ScalingDown"
	SubstatusEndingSlowUpgrading  = "EndingSlowUpgrading"
	SubstatusRollingConfig        = "RollingConfig"

	SubstatusFastScaling       = "FastScaling"
	SubstatusEndingFastScaling = "EndingFastScaling"
	SubstatusScalingPods       = "PodScaling"
	SubstatusScalingRobin      = "RobinScaling"
	SubstatusEndingScaling     = "EndingScaling"
)

var ConditionUpgrading = metav1.Condition{
	Type:               "Upgrading",
	LastTransitionTime: metav1.Now(),
	Message:            "Redkey cluster is upgrading",
	Reason:             "RedkeyClusterUpgrading",
	Status:             metav1.ConditionTrue,
}

var ConditionScalingUp = metav1.Condition{
	Type:               "ScalingUp",
	LastTransitionTime: metav1.Now(),
	Message:            "Redkey cluster is scaling up",
	Reason:             "RedkeyClusterScalingUp",
	Status:             metav1.ConditionTrue,
}
var ConditionScalingDown = metav1.Condition{
	Type:               "ScalingDown",
	LastTransitionTime: metav1.Now(),
	Message:            "Redkey cluster is scaling down",
	Reason:             "RedkeyClusterScalingDown",
	Status:             metav1.ConditionTrue,
}

var AllConditions = []metav1.Condition{ConditionUpgrading, ConditionScalingUp, ConditionScalingDown}

// RedkeyClusterSpec defines the desired state of RedkeyCluster
// +kubebuilder:validation:XValidation:rule="self.ephemeral || has(self.storage)", message="Ephemeral or storage must be set"
// +kubebuilder:validation:XValidation:rule="!(self.ephemeral && has(self.storage))", message="Ephemeral and storage cannot be combined"
type RedkeyClusterSpec struct {
	// +kubebuilder:validation:Optional
	// RedisAuth
	Auth RedisAuth `json:"auth,omitempty"`

	// +kubebuilder:validation:Optional
	// Redis version
	Version string `json:"version,omitempty"`

	// Primaries specifies the number of Redis primary nodes in the cluster.
	// +kubebuilder:validation:Required
	Primaries int32 `json:"primaries"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=0
	// ReplicasPerPrimary specifies how many replicas should be attached to each Redis Primary node
	//+operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Number of replicas per Primary Node"
	ReplicasPerPrimary int32 `json:"replicasPerPrimary,omitempty"`

	// +kubebuilder:validation:Optional
	// Image is the Redis image to use.
	Image string `json:"image,omitempty"`

	// +kubebuilder:validation:Optional
	// DeletePVC specifies if the PVC should be deleted when the RedkeyCluster is deleted.
	DeletePVC bool `json:"deletePVC,omitempty"`

	// +kubebuilder:validation:Optional
	// Backup specifies if the RedkeyCluster should be backed up.
	Backup bool `json:"backup,omitempty"`

	// +kubebuilder:validation:Optional
	// Robin specifies the robin configuration for the RedkeyCluster.
	Robin *RobinSpec `json:"robin,omitempty"`

	// +kubebuilder:validation:Optional
	// PurgeKeysOnRebalance specifies if keys should be purged on rebalance.
	PurgeKeysOnRebalance bool `json:"purgeKeysOnRebalance,omitempty"`

	// +kubebuilder:validation:Optional
	// Config is the Redis configuration to use.
	Config string `json:"config,omitempty"`

	// +kubebuilder:validation:Optional
	// Resources is the resource requirements for the RedkeyCluster.
	Resources *v1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:validation:Optional
	// Labels is the labels to add to the RedkeyCluster.
	Labels *map[string]string `json:"labels,omitempty"`

	// +kubebuilder:validation:Optional
	// Pdb is the PodDisruptionBudget configuration for the RedkeyCluster.
	Pdb Pdb `json:"pdb,omitempty"`

	// +kubebuilder:validation:Optional
	Override *RedkeyClusterOverrideSpec `json:"override,omitempty"`

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

func (redkeyClusterSpec RedkeyClusterSpec) NodesNeeded() int {
	return int(redkeyClusterSpec.Primaries + (redkeyClusterSpec.Primaries * redkeyClusterSpec.ReplicasPerPrimary))
}

// Provides the ability to override the generated manifest of several child resources.
type RedkeyClusterOverrideSpec struct {
	// +kubebuilder:validation:Optional
	// Override configuration for the RedkeyCluster StatefulSet.
	StatefulSet *appsv1.StatefulSet `json:"statefulSet,omitempty"`

	// +kubebuilder:validation:Optional
	// Override configuration for the RedkeyCluster Service.
	Service *v1.Service `json:"service,omitempty"`
}

type RobinSpec struct {
	Template *v1.PodTemplateSpec `json:"template,omitempty"`
	Config   *string             `json:"config,omitempty"`
}

// RedkeyClusterStatus defines the observed state of RedkeyCluster
type RedkeyClusterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make manifests" to regenerate code after modifying this file
	Nodes      map[string]*RedisNode  `json:"nodes"`
	Status     string                 `json:"status"`
	Conditions []metav1.Condition     `json:"conditions,omitempty"`
	Substatus  RedkeyClusterSubstatus `json:"substatus"`
}

type RedkeyClusterSubstatus struct {
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
	IsPrimary bool   `json:"isPrimary"`
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
// +kubebuilder:printcolumn:name="Primaries",type="integer",priority=0,JSONPath=".spec.primaries",description="Amount of Redis primary nodes"
// +kubebuilder:printcolumn:name="Replicas",type="integer",priority=0,JSONPath=".spec.replicasPerPrimary",description="Amount of replicas per primary node"
// +kubebuilder:printcolumn:name="Ephemeral",type="boolean",priority=0,JSONPath=".spec.ephemeral",description="Cluster ephemeral"
// +kubebuilder:printcolumn:name="PurgeKeys",type="boolean",priority=0,JSONPath=".spec.purgeKeysOnRebalance",description="Purge keys on rebalance"
// +kubebuilder:printcolumn:name="Image",type="string",priority=0,JSONPath=".spec.image",description="Source image for Redis instance"
// +kubebuilder:printcolumn:name="Storage",type="string",priority=0,JSONPath=".spec.storage",description="Amount of storage for Redis"
// +kubebuilder:printcolumn:name="StorageClassName",type="string",priority=10,JSONPath=".spec.storageClassName",description="Storage Class to be used by the PVC"
// +kubebuilder:printcolumn:name="DeletePVC",type="boolean",priority=5,JSONPath=".spec.deletePVC",description="Deleve PVC"
// +kubebuilder:printcolumn:name="Status",type="string",priority=0,JSONPath=".status.status",description="The cluster status"
// +kubebuilder:printcolumn:name="Substatus",type="string",priority=0,JSONPath=".status.substatus.status",description="The cluster substatus"
// +kubebuilder:printcolumn:name="Partition",type="string",priority=5,JSONPath=".status.substatus.upgradingPartition",description="Upgrading partition"
// +kubebuilder:subresource:scale:specpath=.spec.primaries,statuspath=.status.primaries,selectorpath=.status.selector
// +kubebuilder:validation:XValidation:rule="self.spec.primaries == oldSelf.spec.primaries || !has(self.status) || self.status.status == 'Ready'", message="Changing the number of primaries is not allowed unless the cluster is in 'Ready' status"
// RedkeyCluster is the Schema for the redkeyclusters API
type RedkeyCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RedkeyClusterSpec   `json:"spec,omitempty"`
	Status            RedkeyClusterStatus `json:"status,omitempty"`
}

func (redkeyCluster RedkeyCluster) NodesNeeded() int {
	return redkeyCluster.Spec.NodesNeeded()
}

//+kubebuilder:object:root=true

// RedkeyClusterList contains a list of RedkeyCluster
type RedkeyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RedkeyCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RedkeyCluster{}, &RedkeyClusterList{})
}

func (redkeyCluster RedkeyCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: redkeyCluster.GetNamespace(),
		Name:      redkeyCluster.GetName(),
	}
}

func CompareStatuses(a, b *RedkeyClusterStatus) bool {
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
		if nodeA.Name != nodeB.Name || nodeA.IP != nodeB.IP || nodeA.IsPrimary != nodeB.IsPrimary || nodeA.ReplicaOf != nodeB.ReplicaOf {
			return false
		}
	}

	return true
}

func IsFastOperationStatus(status RedkeyClusterSubstatus) bool {
	return status.Status == SubstatusFastScaling || status.Status == SubstatusEndingFastScaling ||
		status.Status == SubstatusFastUpgrading || status.Status == SubstatusEndingFastUpgrading
}

func GetRobinStatusCodeEquivalence(redkeyClusterStatus string) string {
	switch redkeyClusterStatus {
	case StatusUpgrading:
		return RobinStatusUpgrading
	case StatusScalingDown:
		return RobinStatusScalingDown
	case StatusScalingUp:
		return RobinStatusScalingUp
	case StatusReady:
		return RobinStatusReady
	case StatusConfiguring:
		return RobinStatusConfiguring
	case StatusInitializing:
		return RobinStatusInitializing
	case StatusError:
		return RobinStatusError
	case StatusMaintenance:
		return RobinStatusMaintenance
	default:
		return RobinStatusError
	}
}
