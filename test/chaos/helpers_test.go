// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/inditextech/redkeyoperator/test/chaos/framework"
)

// startK6OrFail starts a k6 load job and fails the test if it errors.
func startK6OrFail(namespace, clusterName string, duration time.Duration, vus int) string {
	jobName, err := framework.StartK6LoadJob(ctx, k8sClientset, namespace, clusterName, duration, vus)
	Expect(err).NotTo(HaveOccurred(), "failed to start k6 job")
	return jobName
}

// cleanupK6Job safely deletes a k6 job, ignoring errors.
func cleanupK6Job(namespace, jobName string) {
	if jobName == "" {
		return
	}
	_ = framework.DeleteK6Job(ctx, k8sClientset, namespace, jobName)
}

// chaosLoop runs a chaos function repeatedly until the duration expires.
// Reserves time at the end for final checks.
func chaosLoop(duration time.Duration, chaosFn func(iteration int)) {
	endTime := time.Now().Add(duration - chaosReserveTime)
	iteration := 0

	for time.Now().Before(endTime) {
		iteration++
		GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)
		chaosFn(iteration)
	}
}

// verifyClusterHealthy runs all cluster health checks.
func verifyClusterHealthy(namespace, clusterName string) {
	By("verifying cluster readiness")
	Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

	By("verifying all slots assigned")
	Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace, clusterName)).To(Succeed())

	By("verifying no nodes in fail state")
	Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace, clusterName)).To(Succeed())
}

// verifyK6Completed waits for k6 job to complete successfully.
func verifyK6Completed(namespace, jobName string, timeout time.Duration) {
	By("verifying k6 job completed successfully")
	Expect(framework.WaitForK6JobCompletion(ctx, k8sClientset, namespace, jobName, timeout)).To(Succeed())
}

// waitForStatefulSetReplicas polls until the StatefulSet has the expected replica count.
func waitForStatefulSetReplicas(namespace, clusterName string, expectedReplicas int32) {
	Eventually(func() int32 {
		replicas, err := framework.GetStatefulSetReplicas(ctx, k8sClientset, namespace, clusterName)
		if err != nil {
			return -1
		}
		return replicas
	}, scaleAckTimeout, scalePollInterval).Should(Equal(expectedReplicas),
		fmt.Sprintf("StatefulSet should have %d replicas", expectedReplicas))
}
