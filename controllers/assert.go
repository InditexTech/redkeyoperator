package controllers

import (
	"fmt"
	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func assertStatusEqual(t *testing.T, redis *redisv1.RedisCluster, expectedStatus string) {
	got := redis.Status.Status
	if expectedStatus != got {
		fmt.Printf("expected status '%s' but got '%s'\n", expectedStatus, got)
		t.Fail()
	}
}

func assertConditionTrue(t *testing.T, redis *redisv1.RedisCluster, condition metav1.Condition) {
	conditions := redis.Status.Conditions
	if !(meta.IsStatusConditionTrue(conditions, condition.Type)) {
		fmt.Printf("expected condition '%s' to be true but conditions were '%v'\n", condition.Type, conditions)
		t.Fail()
	}
}
func assertConditionFalse(t *testing.T, redis *redisv1.RedisCluster, condition metav1.Condition) {
	conditions := redis.Status.Conditions
	if meta.IsStatusConditionTrue(conditions, condition.Type) {
		fmt.Printf("expected condition '%s' to be false but conditions were '%v'\n", condition.Type, conditions)
		t.Fail()
	}
}
