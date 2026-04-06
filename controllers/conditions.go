// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"github.com/go-logr/logr"
	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *RedkeyClusterReconciler) setConditionTrue(rc *redkeyv1.RedkeyCluster, condition metav1.Condition, message string) {
	condition.ObservedGeneration = rc.Generation
	condition.Message = message
	alreadyTrue := meta.IsStatusConditionTrue(rc.Status.Conditions, condition.Type)
	if meta.SetStatusCondition(&rc.Status.Conditions, condition) && !alreadyTrue {
        r.Recorder.Event(rc, "Normal", condition.Reason, message)		// Event generated only when condition changes to true
    }
}

func setConditionFalse(log logr.Logger, redkeyCluster *redkeyv1.RedkeyCluster, condition metav1.Condition) {
	condition.Status = metav1.ConditionFalse
	condition.ObservedGeneration = redkeyCluster.Generation

	if meta.SetStatusCondition(&redkeyCluster.Status.Conditions, condition) {
		log.Info("Condition set to false", "condition", condition)
	}
}

func setAllConditionsFalse(log logr.Logger, redkeyCluster *redkeyv1.RedkeyCluster) {
	for _, condition := range redkeyv1.AllConditions {
		setConditionFalse(log, redkeyCluster, condition)
	}
}
