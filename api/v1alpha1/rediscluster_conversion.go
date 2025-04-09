// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"fmt"

	"github.com/inditextech/redisoperator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
)

// ConvertTo converts this RedisCluster to the Hub version (v1).
func (src *RedisCluster) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1.RedisCluster)
	ctrl.Log.Info(fmt.Sprintf("Converting RedisCluster %s from v1alpha1 version to v1 version", src.Name))

	return src.toV1(dst)
}

// ConvertFrom converts from the Hub version (v1) to this version.
func (dst *RedisCluster) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1.RedisCluster)
	ctrl.Log.Info(fmt.Sprintf("Converting RedisCluster %s from v1 version to v1alpha1 version", src.Name))

	return dst.fromV1(src)
}

func (src *RedisCluster) toV1(dst *v1.RedisCluster) error {
	// Metadata
	src.copyMetatoV1(dst)

	// Spec
	src.copySpectoV1(dst)

	// Status
	src.copyStatustoV1(dst)

	// Override
	dst.Spec.Override = &v1.RedisClusterOverrideSpec{}

	// Extra annotations
	src.copyExtraAnnotationsToV1(dst)

	// Extra labels
	src.copyExtraLabelsToV1(dst)

	return nil
}

func (src *RedisCluster) copyMetatoV1(dst *v1.RedisCluster) {
	dst.TypeMeta = metav1.TypeMeta{
		APIVersion: v1.GroupVersion.String(),
		Kind:       "RedisCluster",
	}
	dst.ObjectMeta = src.ObjectMeta
}

func (src *RedisCluster) copySpectoV1(dst *v1.RedisCluster) {
	dst.Spec.Auth = v1.RedisAuth{SecretName: src.Spec.Auth.SecretName}
	dst.Spec.Version = src.Spec.Version
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.ReplicasPerMaster = src.Spec.ReplicasPerMaster
	dst.Spec.Image = src.Spec.Image
	dst.Spec.Ephemeral = src.Spec.Ephemeral
	dst.Spec.Backup = src.Spec.Backup
	dst.Spec.Storage = src.Spec.Storage
	dst.Spec.Monitoring = &v1.MonitoringSpec{
		Template: src.Spec.Monitoring,
	}
	dst.Spec.PurgeKeysOnRebalance = src.Spec.PurgeKeysOnRebalance
	dst.Spec.Config = src.Spec.Config
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Labels = src.Spec.Labels
	dst.Spec.Pdb = v1.Pdb(src.Spec.Pdb)
	dst.Spec.StorageClassName = src.Spec.StorageClassName
	dst.Spec.DeletePVC = src.Spec.DeletePVC
	dst.Spec.AccessModes = src.Spec.AccessModes
}

func (src *RedisCluster) copyStatustoV1(dst *v1.RedisCluster) {
	dst.Status.Status = src.Status.Status
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.Nodes = make(map[string]*v1.RedisNode)

	for node := range src.Status.Nodes {
		dst.Status.Nodes[node] = &v1.RedisNode{
			Name: src.Status.Nodes[node].NodeName,
			IP:   src.Status.Nodes[node].IP,
		}
	}
}

func (src *RedisCluster) copyExtraAnnotationsToV1(dst *v1.RedisCluster) {
	// Get the value of the annotation
	value, ok := src.Annotations["inditex.com/nodes-extra-annotations"]
	if !ok {
		return
	}

	// Unmarshal the JSON value into a map
	var nodesExtraAnnotations map[string]string
	if err := json.Unmarshal([]byte(value), &nodesExtraAnnotations); err != nil {
		return
	}

	// If the StatefulSet override is nil, create it
	if dst.Spec.Override.StatefulSet == nil {
		dst.Spec.Override.StatefulSet = &appsv1.StatefulSet{}
		dst.Spec.Override.StatefulSet.Spec.Template = corev1.PodTemplateSpec{}
		dst.Spec.Override.StatefulSet.Spec.Template.Annotations = make(map[string]string)
	}

	// Copy the annotations to the StatefulSet template
	for k, v := range nodesExtraAnnotations {
		dst.Spec.Override.StatefulSet.Spec.Template.Annotations[k] = v
	}

	// Remove the annotation from the source object
	delete(src.Annotations, "inditex.com/nodes-extra-annotations")
}

func (src *RedisCluster) copyExtraLabelsToV1(dst *v1.RedisCluster) {
	// Get the value of the annotation
	value, ok := src.Annotations["inditex.com/nodes-extra-labels"]
	if !ok {
		return
	}

	// Unmarshal the JSON value into a map
	var nodesExtraLabels map[string]string
	if err := json.Unmarshal([]byte(value), &nodesExtraLabels); err != nil {
		return
	}

	// If the StatefulSet override is nil, create it
	if dst.Spec.Override.StatefulSet == nil {
		dst.Spec.Override.StatefulSet = &appsv1.StatefulSet{}
		dst.Spec.Override.StatefulSet.Spec.Template = corev1.PodTemplateSpec{}
		dst.Spec.Override.StatefulSet.Spec.Template.Labels = make(map[string]string)
	}

	// Copy the labels to the StatefulSet template
	for k, v := range nodesExtraLabels {
		dst.Spec.Override.StatefulSet.Spec.Template.Labels[k] = v
	}

	// Remove the annotation from the source object
	delete(src.Annotations, "inditex.com/nodes-extra-labels")
}

func (dst *RedisCluster) fromV1(src *v1.RedisCluster) error {
	// Metadata
	dst.copyMetaFromV1(src)

	// Spec
	dst.copySpecFromV1(src)

	// Status
	dst.copyStatusFromV1(src)

	// Override
	if src.Spec.Override == nil {
		return nil
	}

	// Fields from statefulset override
	if src.Spec.Override.StatefulSet != nil {
		// Extra annotations
		dst.copyExtraAnnotationsFromV1(src)

		// Extra labels
		dst.copyExtraLabelsFromV1(src)
	}

	return nil
}

func (dst *RedisCluster) copyMetaFromV1(src *v1.RedisCluster) {
	dst.TypeMeta = metav1.TypeMeta{
		APIVersion: GroupVersion.String(),
		Kind:       "RedisCluster",
	}
	dst.ObjectMeta = src.ObjectMeta
}

func (dst *RedisCluster) copySpecFromV1(src *v1.RedisCluster) {
	dst.Spec.Auth = RedisAuth{SecretName: src.Spec.Auth.SecretName}
	dst.Spec.Version = src.Spec.Version
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.ReplicasPerMaster = src.Spec.ReplicasPerMaster
	dst.Spec.Image = src.Spec.Image
	dst.Spec.Ephemeral = src.Spec.Ephemeral
	dst.Spec.Backup = src.Spec.Backup
	dst.Spec.Storage = src.Spec.Storage
	dst.Spec.PurgeKeysOnRebalance = src.Spec.PurgeKeysOnRebalance
	dst.Spec.Config = src.Spec.Config
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Labels = src.Spec.Labels
	dst.Spec.Pdb = Pdb(src.Spec.Pdb)
	dst.Spec.StorageClassName = src.Spec.StorageClassName
	dst.Spec.DeletePVC = src.Spec.DeletePVC
	dst.Spec.AccessModes = src.Spec.AccessModes

	if src.Spec.Monitoring != nil && src.Spec.Monitoring.Template != nil {
		dst.Spec.Monitoring = src.Spec.Monitoring.Template
	}
}

func (dst *RedisCluster) copyStatusFromV1(src *v1.RedisCluster) {
	dst.Status.Status = src.Status.Status
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.Nodes = make(map[string]*RedisNode)

	for node := range src.Status.Nodes {
		dst.Status.Nodes[node] = &RedisNode{
			NodeName: src.Status.Nodes[node].Name,
			NodeID:   node,
			IP:       src.Status.Nodes[node].IP,
		}
	}

	dst.Status.Slots = []*SlotRange{}
}

func (dst *RedisCluster) copyExtraAnnotationsFromV1(src *v1.RedisCluster) {
	// Get the extra annotations from pod template annotations of override
	nodesExtraAnnotations := make(map[string]string)
	for k, v := range src.Spec.Override.StatefulSet.Spec.Template.Annotations {
		nodesExtraAnnotations[k] = v
	}

	// If there are no extra annotations, return
	if len(nodesExtraAnnotations) == 0 {
		return
	}

	// Marshal the extra annotations to JSON
	nodesExtraAnnotationsJSON, err := json.Marshal(nodesExtraAnnotations)
	if err != nil {
		return
	}

	// If the annotations map is nil, create it
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}

	// Set the annotation on the destination object
	dst.Annotations["inditex.com/nodes-extra-annotations"] = string(nodesExtraAnnotationsJSON)
}

func (dst *RedisCluster) copyExtraLabelsFromV1(src *v1.RedisCluster) {
	// Get the extra labels from pod template labels of override
	nodesExtraLabels := make(map[string]string)
	for k, v := range src.Spec.Override.StatefulSet.Spec.Template.Labels {
		nodesExtraLabels[k] = v
	}

	// If there are no extra labels, return
	if len(nodesExtraLabels) == 0 {
		return
	}

	// Marshal the extra labels to JSON
	nodesExtraLabelsJSON, err := json.Marshal(nodesExtraLabels)
	if err != nil {
		return
	}

	// If the annotations map is nil, create it
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}

	// Set the annotation on the destination object
	dst.Annotations["inditex.com/nodes-extra-labels"] = string(nodesExtraLabelsJSON)
}
