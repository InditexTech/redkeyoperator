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
	RobinStatusUnknown       = "Unknown"

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

// +kubebuilder:object:root=true
// RedkeyClusterList contains a list of RedkeyCluster
type RedkeyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RedkeyCluster `json:"items"`
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

// NamespacedName returns the NamespacedName of the RedkeyCluster.
func (redkeyCluster RedkeyCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: redkeyCluster.GetNamespace(),
		Name:      redkeyCluster.GetName(),
	}
}

// RedkeyClusterSpec defines the desired state of RedkeyCluster.
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
	ReplicasPerPrimary int32 `json:"replicasPerPrimary"`

	// +kubebuilder:validation:Optional
	// Image is the Redis image to use.
	Image string `json:"image,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	// DeletePVC specifies if the PVC should be deleted when the RedkeyCluster is deleted.
	DeletePVC *bool `json:"deletePVC"`

	// +kubebuilder:validation:Optional
	// Backup specifies if the RedkeyCluster should be backed up.
	Backup bool `json:"backup,omitempty"`

	// +kubebuilder:validation:Optional
	// Robin specifies the robin configuration for the RedkeyCluster.
	Robin *RobinSpec `json:"robin,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	// PurgeKeysOnRebalance specifies if keys should be purged on rebalance.
	PurgeKeysOnRebalance *bool `json:"purgeKeysOnRebalance"`

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

// RobinSpec defines the desired state of Robin configuration for RedkeyCluster.
type RobinSpec struct {
	Template *v1.PodTemplateSpec `json:"template,omitempty"`
	Config   *RobinConfig        `json:"config,omitempty"`
}

type RobinConfig struct {
	Reconciler *RobinConfigReconciler `json:"reconciler,omitempty"`
	Cluster    *RobinConfigCluster    `json:"cluster,omitempty"`
	Metrics    *RobinConfigMetrics    `json:"metrics,omitempty"`
}

type RobinConfigReconciler struct {
	IntervalSeconds                 *int `json:"intervalSeconds,omitempty"`
	OperationCleanUpIntervalSeconds *int `json:"operationCleanUpIntervalSeconds,omitempty"`
}

type RobinConfigCluster struct {
	HealthProbePeriodSeconds *int `json:"healthProbePeriodSeconds,omitempty"`
	HealingTimeSeconds       *int `json:"healingTimeSeconds,omitempty"`
	MaxRetries               *int `json:"maxRetries,omitempty"`
	BackOff                  *int `json:"backOff,omitempty"`
}

type RobinConfigMetrics struct {
	IntervalSeconds *int     `json:"intervalSeconds,omitempty"`
	RedisInfoKeys   []string `json:"redisInfoKeys,omitempty"`
}

// RedisAuth defines the authentication configuration for Redis.
type RedisAuth struct {
	SecretName string `json:"secret,omitempty"`
}

// Pdb defines the PodDisruptionBudget configuration for RedkeyCluster.
type Pdb struct {
	Enabled            bool               `json:"enabled,omitempty"`
	PdbSizeUnavailable intstr.IntOrString `json:"pdbSizeUnavailable,omitempty"`
	PdbSizeAvailable   intstr.IntOrString `json:"pdbSizeAvailable,omitempty"`
}

// NodesNeeded returns the total number of nodes needed for the RedkeyCluster.
func (redkeyClusterSpec RedkeyClusterSpec) NodesNeeded() int {
	return int(redkeyClusterSpec.Primaries + (redkeyClusterSpec.Primaries * redkeyClusterSpec.ReplicasPerPrimary))
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

// RedisNode defines a Redis node in the RedkeyCluster.
type RedisNode struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	IsPrimary bool   `json:"isPrimary"`
	ReplicaOf string `json:"replicaOf"`
}

// RedkeyClusterSubstatus defines the substatus of the RedkeyCluster.
type RedkeyClusterSubstatus struct {
	Status             string `json:"status,omitempty"`
	UpgradingPartition string `json:"upgradingPartition,omitempty"`
}

// Provides the ability to override the generated manifest of several child resources.
type RedkeyClusterOverrideSpec struct {
	// +kubebuilder:validation:Optional
	// Override configuration for the RedkeyCluster StatefulSet.
	StatefulSet *PartialStatefulSet `json:"statefulSet,omitempty"`

	// +kubebuilder:validation:Optional
	// Override configuration for the RedkeyCluster Service.
	Service *PartialService `json:"service,omitempty"`
}

// PartialStatefulSet is a reduced representation of apps/v1 StatefulSet that intentionally omits the Status field so that
// it is not required in CRD schemas when used as an override by users.
// +kubebuilder:pruning:PreserveUnknownFields
//
// The type-level pruning directive tells controller-gen to mark the entire object as "preserve unknown fields" in the CRD so consumers can provide
// partial objects (e.g. only `spec.template.spec.tolerations`) without being forced to set nested required fields like `selector` or `containers`.
type PartialStatefulSet struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Metadata metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// Spec is optional, allowing partial overrides without requiring any fields.
	Spec *PartialStatefulSetSpec `json:"spec,omitempty"`
}

// PartialStatefulSetSpec is a partial representation of StatefulSetSpe where all fields are optional,
// allowing users to override only specific fields.
// +kubebuilder:pruning:PreserveUnknownFields
type PartialStatefulSetSpec struct {
	// +kubebuilder:validation:Optional
	MinReadySeconds *int32 `json:"minReadySeconds,omitempty"`

	// +kubebuilder:validation:Optional
	Replicas *int32 `json:"replicas,omitempty"`

	// +kubebuilder:validation:Optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// +kubebuilder:validation:Optional
	ServiceName string `json:"serviceName,omitempty"`

	// +kubebuilder:validation:Optional
	Template *PartialPodTemplateSpec `json:"template,omitempty"`

	// +kubebuilder:validation:Optional
	UpdateStrategy *appsv1.StatefulSetUpdateStrategy `json:"updateStrategy,omitempty"`

	// +kubebuilder:validation:Optional
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`

	// +kubebuilder:validation:Optional
	PodManagementPolicy string `json:"podManagementPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	VolumeClaimTemplates []v1.PersistentVolumeClaim `json:"volumeClaimTemplates,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	PersistentVolumeClaimRetentionPolicy *appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy `json:"persistentVolumeClaimRetentionPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Ordinals *appsv1.StatefulSetOrdinals `json:"ordinals,omitempty"`
}

// PartialPodTemplateSpec is a partial representation of PodTemplateSpec where containers and other fields are optional,
// allowing users to override only specific pod template fields without requiring containers to be defined.
// +kubebuilder:pruning:PreserveUnknownFields
type PartialPodTemplateSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Metadata metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Spec PartialPodSpec `json:"spec,omitempty"`
}

// PartialPodSpec is a partial representation of PodSpec where containers are optional instead of required.
// +kubebuilder:pruning:PreserveUnknownFields
type PartialPodSpec struct {
	// +kubebuilder:validation:Optional
	Containers []v1.Container `json:"containers,omitempty"`

	// +kubebuilder:validation:Optional
	InitContainers []v1.Container `json:"initContainers,omitempty"`

	// +kubebuilder:validation:Optional
	EphemeralContainers []v1.EphemeralContainer `json:"ephemeralContainers,omitempty"`

	// +kubebuilder:validation:Optional
	RestartPolicy v1.RestartPolicy `json:"restartPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// +kubebuilder:validation:Optional
	DNSPolicy v1.DNSPolicy `json:"dnsPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// +kubebuilder:validation:Optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// +kubebuilder:validation:Optional
	NodeName string `json:"nodeName,omitempty"`

	// +kubebuilder:validation:Optional
	HostNetwork bool `json:"hostNetwork,omitempty"`

	// +kubebuilder:validation:Optional
	HostPID bool `json:"hostPID,omitempty"`

	// +kubebuilder:validation:Optional
	HostIPC bool `json:"hostIPC,omitempty"`

	// +kubebuilder:validation:Optional
	ShareProcessNamespace *bool `json:"shareProcessNamespace,omitempty"`

	// +kubebuilder:validation:Optional
	SecurityContext *v1.PodSecurityContext `json:"securityContext,omitempty"`

	// +kubebuilder:validation:Optional
	ImagePullSecrets []v1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// +kubebuilder:validation:Optional
	Hostname string `json:"hostname,omitempty"`

	// +kubebuilder:validation:Optional
	Subdomain string `json:"subdomain,omitempty"`

	// +kubebuilder:validation:Optional
	Affinity *v1.Affinity `json:"affinity,omitempty"`

	// +kubebuilder:validation:Optional
	SchedulerName string `json:"schedulerName,omitempty"`

	// +kubebuilder:validation:Optional
	Tolerations []v1.Toleration `json:"tolerations,omitempty"`

	// +kubebuilder:validation:Optional
	HostAliases []v1.HostAlias `json:"hostAliases,omitempty"`

	// +kubebuilder:validation:Optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// +kubebuilder:validation:Optional
	Priority *int32 `json:"priority,omitempty"`

	// +kubebuilder:validation:Optional
	DNSConfig *v1.PodDNSConfig `json:"dnsConfig,omitempty"`

	// +kubebuilder:validation:Optional
	ReadinessGates []v1.PodReadinessGate `json:"readinessGates,omitempty"`

	// +kubebuilder:validation:Optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`

	// +kubebuilder:validation:Optional
	EnableServiceLinks *bool `json:"enableServiceLinks,omitempty"`

	// +kubebuilder:validation:Optional
	PreemptionPolicy *v1.PreemptionPolicy `json:"preemptionPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	Overhead v1.ResourceList `json:"overhead,omitempty"`

	// +kubebuilder:validation:Optional
	TopologySpreadConstraints []v1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// +kubebuilder:validation:Optional
	SecurityGroups []int64 `json:"securityGroups,omitempty"`

	// +kubebuilder:validation:Optional
	WindowsOptions *v1.WindowsSecurityContextOptions `json:"windowsOptions,omitempty"`

	// +kubebuilder:validation:Optional
	FSGroup *int64 `json:"fsGroup,omitempty"`

	// +kubebuilder:validation:Optional
	Volumes []v1.Volume `json:"volumes,omitempty"`

	// +kubebuilder:validation:Optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// +kubebuilder:validation:Optional
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken,omitempty"`
}

// PartialService is a reduced representation of core/v1 Service that allows preserving unknown fields in metadata (like annotations and labels).
// +kubebuilder:pruning:PreserveUnknownFields
//
// Same rationale as PartialStatefulSet: preserve unknown fields at the type-level so partial Service overrides (e.g. only `metadata.annotations`)
// are accepted by server-side validation.
type PartialService struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Metadata metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// Spec is optional, allowing partial overrides without requiring any fields.
	Spec *PartialServiceSpec `json:"spec,omitempty"`
}

// PartialServiceSpec is a partial representation of ServiceSpec where all fields are optional, allowing users to override only specific fields.
// +kubebuilder:pruning:PreserveUnknownFields
type PartialServiceSpec struct {
	// +kubebuilder:validation:Optional
	Ports []v1.ServicePort `json:"ports,omitempty"`

	// +kubebuilder:validation:Optional
	Selector map[string]string `json:"selector,omitempty"`

	// +kubebuilder:validation:Optional
	ClusterIP string `json:"clusterIP,omitempty"`

	// +kubebuilder:validation:Optional
	ClusterIPs []string `json:"clusterIPs,omitempty"`

	// +kubebuilder:validation:Optional
	Type v1.ServiceType `json:"type,omitempty"`

	// +kubebuilder:validation:Optional
	ExternalIPs []string `json:"externalIPs,omitempty"`

	// +kubebuilder:validation:Optional
	SessionAffinity v1.ServiceAffinity `json:"sessionAffinity,omitempty"`

	// +kubebuilder:validation:Optional
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`

	// +kubebuilder:validation:Optional
	LoadBalancerSourceRanges []string `json:"loadBalancerSourceRanges,omitempty"`

	// +kubebuilder:validation:Optional
	ExternalName string `json:"externalName,omitempty"`

	// +kubebuilder:validation:Optional
	ExternalTrafficPolicy v1.ServiceExternalTrafficPolicy `json:"externalTrafficPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	HealthCheckNodePort int32 `json:"healthCheckNodePort,omitempty"`

	// +kubebuilder:validation:Optional
	PublishNotReadyAddresses bool `json:"publishNotReadyAddresses,omitempty"`

	// +kubebuilder:validation:Optional
	SessionAffinityConfig *v1.SessionAffinityConfig `json:"sessionAffinityConfig,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	IPFamilies []v1.IPFamily `json:"ipFamilies,omitempty"`

	// +kubebuilder:validation:Optional
	IPFamilyPolicy *v1.IPFamilyPolicy `json:"ipFamilyPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	AllocateLoadBalancerNodePorts *bool `json:"allocateLoadBalancerNodePorts,omitempty"`
}

// SlotRange defines a range of slots in the Redis cluster.
type SlotRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// init function to register the RedkeyCluster and RedkeyClusterList types with the scheme.
func init() {
	SchemeBuilder.Register(&RedkeyCluster{}, &RedkeyClusterList{})
}

// NodesNeeded returns the total number of nodes needed for the RedkeyCluster.
func (redkeyCluster RedkeyCluster) NodesNeeded() int {
	return redkeyCluster.Spec.NodesNeeded()
}

// CompareStatuses compares two RedkeyClusterStatus objects and returns true if they are equal.
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

// IsFastOperationStatus returns true if the RedkeyClusterSubstatus indicates a fast operation (scaling or upgrading).
func IsFastOperationStatus(status RedkeyClusterSubstatus) bool {
	return status.Status == SubstatusFastScaling || status.Status == SubstatusEndingFastScaling ||
		status.Status == SubstatusFastUpgrading || status.Status == SubstatusEndingFastUpgrading
}

// GetRobinStatusCodeEquivalence maps RedkeyCluster status to Robin status codes.
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
