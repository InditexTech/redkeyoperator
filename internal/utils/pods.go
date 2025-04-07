package utils

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodRunningReady checks if the provided pod is running and has a condition of PodReady.
// Returns true if these conditions are met, or an error detailing the specific unmet condition.
func PodRunningReady(p *corev1.Pod) (bool, error) {
	if p == nil {
		return false, fmt.Errorf("provided pod is nil")
	}

	// Check if the pod phase is running.
	if p.Status.Phase != corev1.PodRunning {
		return false, fmt.Errorf("expected pod '%s' on node '%s' to be '%v', but it was '%v'",
			p.ObjectMeta.Name, p.Spec.NodeName, corev1.PodRunning, p.Status.Phase)
	}

	// Check if the ready condition is true.
	_, condition := GetPodCondition(&p.Status, corev1.PodReady)
	if !(condition != nil && condition.Status == corev1.ConditionTrue) {
		return false, fmt.Errorf("pod '%s' on node '%s' does not have condition '%v=%v'; current conditions: %v",
			p.ObjectMeta.Name, p.Spec.NodeName, corev1.PodReady, corev1.ConditionTrue, p.Status.Conditions)
	}

	return true, nil
}

// GetPodCondition returns the index and the reference to the pod condition of the specified type.
// Returns -1 and nil if the condition is not found or if the status is nil.
func GetPodCondition(status *corev1.PodStatus, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return i, &status.Conditions[i]
		}
	}
	return -1, nil
}

// AllPodsReady checks if the expected number of pods are ready.
func AllPodsReady(ctx context.Context, client client.Client, listOptions *client.ListOptions, expectedReadyReplicas int) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("client is nil")
	}
	if listOptions == nil {
		return false, fmt.Errorf("listOptions is nil")
	}

	pods := &corev1.PodList{}
	err := client.List(ctx, pods, listOptions)
	if err != nil {
		return false, err
	}
	current := 0
	for _, pod := range pods.Items {
		flag, err := PodRunningReady(&pod)
		if err == nil && flag {
			current++
		}
	}
	return current == expectedReadyReplicas, nil
}

type PodReadyWait struct {
	Client client.Client
}

func (prw PodReadyWait) WaitForPodsToBecomeReady(ctx context.Context, interval, timeout time.Duration, listOptions *client.ListOptions, expectedReadyReplicas int) error {

	return wait.PollUntilContextTimeout(ctx, interval, timeout, false, func(ctx context.Context) (bool, error) {
		pods := &corev1.PodList{}
		if err := prw.Client.List(ctx, pods, listOptions); err != nil {
			return false, err
		}

		var current int
		for _, pod := range pods.Items {
			if flag, err := PodRunningReady(&pod); err == nil && flag {
				current++
			}
		}

		return current == expectedReadyReplicas, nil
	})
}
