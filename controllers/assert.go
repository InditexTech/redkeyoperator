// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"testing"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func assertStatusEqual(t *testing.T, redis *redkeyv1.RedkeyCluster, expectedStatus string) {
	got := redis.Status.Status
	if expectedStatus != got {
		fmt.Printf("expected status '%s' but got '%s'\n", expectedStatus, got)
		t.Fail()
	}
}

func assertConditionTrue(t *testing.T, redis *redkeyv1.RedkeyCluster, condition metav1.Condition) {
	conditions := redis.Status.Conditions
	if !(meta.IsStatusConditionTrue(conditions, condition.Type)) {
		fmt.Printf("expected condition '%s' to be true but conditions were '%v'\n", condition.Type, conditions)
		t.Fail()
	}
}
func assertConditionFalse(t *testing.T, redis *redkeyv1.RedkeyCluster, condition metav1.Condition) {
	conditions := redis.Status.Conditions
	if meta.IsStatusConditionTrue(conditions, condition.Type) {
		fmt.Printf("expected condition '%s' to be false but conditions were '%v'\n", condition.Type, conditions)
		t.Fail()
	}
}
