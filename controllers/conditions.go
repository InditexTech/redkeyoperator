// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"github.com/go-logr/logr"
	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func SetConditionFalse(log logr.Logger, redisCluster *redisv1.RedisCluster, condition metav1.Condition) {
	condition.Status = metav1.ConditionFalse
	changed := meta.SetStatusCondition(&redisCluster.Status.Conditions, condition)
	if changed {
		log.Info("Condition set to false", "condition", condition)
	}
}

func SetAllConditionsFalse(log logr.Logger, redisCluster *redisv1.RedisCluster) {
	for _, condition := range redisv1.AllConditions {
		SetConditionFalse(log, redisCluster, condition)
	}
}
