// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// RedkeyClusterGVR is the GroupVersionResource for RedkeyCluster CRD.
var RedkeyClusterGVR = schema.GroupVersionResource{
	Group:    "redis.inditex.dev",
	Version:  "v1",
	Resource: "redkeyclusters",
}

// GetRedkeyCluster retrieves a RedkeyCluster by name.
func GetRedkeyCluster(ctx context.Context, dc dynamic.Interface, namespace, name string) (*redkeyv1.RedkeyCluster, error) {
	unstr, err := dc.Resource(RedkeyClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return unstructuredToRedkeyCluster(unstr)
}

// CreateRedkeyClusterCR creates a RedkeyCluster CR using dynamic client.
func CreateRedkeyClusterCR(ctx context.Context, dc dynamic.Interface, rc *redkeyv1.RedkeyCluster) error {
	unstr, err := redkeyClusterToUnstructured(rc)
	if err != nil {
		return fmt.Errorf("convert to unstructured: %w", err)
	}

	_, err = dc.Resource(RedkeyClusterGVR).Namespace(rc.Namespace).Create(ctx, unstr, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create RedkeyCluster %s/%s: %w", rc.Namespace, rc.Name, err)
	}
	return nil
}

// UpdateRedkeyClusterCR updates an existing RedkeyCluster CR.
func UpdateRedkeyClusterCR(ctx context.Context, dc dynamic.Interface, rc *redkeyv1.RedkeyCluster) error {
	unstr, err := redkeyClusterToUnstructured(rc)
	if err != nil {
		return fmt.Errorf("convert to unstructured: %w", err)
	}

	_, err = dc.Resource(RedkeyClusterGVR).Namespace(rc.Namespace).Update(ctx, unstr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update RedkeyCluster %s/%s: %w", rc.Namespace, rc.Name, err)
	}
	return nil
}

// EnsureRedkeyCluster creates or updates a RedkeyCluster CR.
func EnsureRedkeyCluster(ctx context.Context, dc dynamic.Interface, rc *redkeyv1.RedkeyCluster) error {
	existing, err := GetRedkeyCluster(ctx, dc, rc.Namespace, rc.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			return CreateRedkeyClusterCR(ctx, dc, rc)
		}
		return err
	}

	// Update existing - preserve resourceVersion
	rc.ResourceVersion = existing.ResourceVersion
	return UpdateRedkeyClusterCR(ctx, dc, rc)
}

// ScaleRedkeyCluster updates the Primaries field of a RedkeyCluster.
func ScaleRedkeyCluster(ctx context.Context, dc dynamic.Interface, namespace, name string, primaries int32) error {
	rc, err := GetRedkeyCluster(ctx, dc, namespace, name)
	if err != nil {
		return fmt.Errorf("get RedkeyCluster: %w", err)
	}

	rc.Spec.Primaries = primaries
	return UpdateRedkeyClusterCR(ctx, dc, rc)
}

// redkeyClusterToUnstructured converts a RedkeyCluster to unstructured.
func redkeyClusterToUnstructured(rc *redkeyv1.RedkeyCluster) (*unstructured.Unstructured, error) {
	// Ensure TypeMeta is set
	rc.TypeMeta = metav1.TypeMeta{
		APIVersion: "redis.inditex.dev/v1",
		Kind:       "RedkeyCluster",
	}

	data, err := json.Marshal(rc)
	if err != nil {
		return nil, err
	}

	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &obj.Object); err != nil {
		return nil, err
	}
	return obj, nil
}

// unstructuredToRedkeyCluster converts unstructured to RedkeyCluster.
func unstructuredToRedkeyCluster(unstr *unstructured.Unstructured) (*redkeyv1.RedkeyCluster, error) {
	rc := &redkeyv1.RedkeyCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstr.Object, rc); err != nil {
		return nil, err
	}
	return rc, nil
}
