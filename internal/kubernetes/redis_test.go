// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes_test

import (
	"context"
	"testing"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/redis"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFindExistingStatefulSetFindsCorrectStatefulSet(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "redis-cluster",
				Namespace: "default",
				Labels: map[string]string{
					"app": "redis",
				},
			},
			Spec: appsv1.StatefulSetSpec{},
		}).Build()
	request := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "redis-cluster",
			Namespace: "default",
		},
	}
	statefulset, err := kubernetes.FindExistingStatefulSet(context.TODO(), fakeClient, request)
	if err != nil {
		t.Errorf("Received unexpected error, when fetching statefulset. Error: [%v]", err)
		t.FailNow()
	}
	if statefulset.Name != "redis-cluster" {
		t.Error("message", "Received incorrect statefulset", "got", statefulset, "error", err)
	}
}

func TestFindExistingStatefulSetReturnsNotFoundForCases(t *testing.T) {
	testMap := map[string]struct {
		client client.Client
	}{
		"no-objects-exist": {
			client: fake.NewClientBuilder().Build(),
		},
		"objects-in-different-namespace-exist": {
			client: fake.NewClientBuilder().WithObjects(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis-cluster",
					Namespace: "foobar",
					Labels: map[string]string{
						"app": "redis",
					},
				},
				Spec: appsv1.StatefulSetSpec{},
			}).Build(),
		},
		"objects-with-different-name": {
			client: fake.NewClientBuilder().WithObjects(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis-cluster-2",
					Namespace: "default",
					Labels: map[string]string{
						"app": "redis",
					},
				},
				Spec: appsv1.StatefulSetSpec{},
			}).Build(),
		},
	}
	request := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "redis-cluster",
			Namespace: "default",
		},
	}
	for name, test := range testMap {
		_, err := kubernetes.FindExistingStatefulSet(context.TODO(), test.client, request)
		if err == nil {
			// We are expecting a Not Found error. No errors means we we found the wrong thing
			t.Errorf("Expected an not found error, but did not get an error at all while running case: [%s]", name)
			t.FailNow()
		}
		if !errors.IsNotFound(err) {
			t.Errorf("Got an unexpected error while running case: [%s]. Expecting Not Found error, but got: [%v]", name, err)
			t.FailNow()
		}
	}
}

func TestGetStatefulSetSelectorLabelReturnsCorrectLabels(t *testing.T) {
	testMap := map[string]struct {
		client           client.Client
		expectedLabelKey string
	}{
		"no-objects-exist": {
			client:           fake.NewClientBuilder().Build(),
			expectedLabelKey: redis.RedisClusterComponentLabel,
		},
		"objects-with-app-label": {
			client: fake.NewClientBuilder().WithObjects(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis-cluster",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "redis",
							},
						},
					},
				},
			}).Build(),
			expectedLabelKey: "app",
		},
		"objects-with-component-label": {
			client: fake.NewClientBuilder().WithObjects(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis-cluster",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								redis.RedisClusterComponentLabel: "redis",
							},
						},
					},
				},
			}).Build(),
			expectedLabelKey: redis.RedisClusterComponentLabel,
		},
		"objects-with-both-labels": {
			client: fake.NewClientBuilder().WithObjects(&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis-cluster",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								redis.RedisClusterComponentLabel: "redis",
								"app":                            "redis",
							},
						},
					},
				},
			}).Build(),
			expectedLabelKey: redis.RedisClusterComponentLabel,
		},
	}
	for name, test := range testMap {
		labelKey := kubernetes.GetStatefulSetSelectorLabel(context.TODO(), test.client, &redisv1.RedisCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "redis-cluster",
				Namespace: "default",
			},
		})
		if labelKey != test.expectedLabelKey {
			// We are expecting a Not Found error. No errors means we we found the wrong thing
			t.Errorf("Incorrect label key received for case {%s}. Got {%s}, Expected {%s}", name, labelKey, test.expectedLabelKey)
		}
	}
}
