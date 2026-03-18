// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"fmt"
	"math/rand"
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

// cleanupK6Job deletes a k6 job and fails the spec if cleanup fails.
func cleanupK6Job(namespace, jobName string) {
	if jobName == "" {
		return
	}
	Expect(framework.DeleteK6Job(ctx, k8sClientset, namespace, jobName)).To(Succeed(), "failed to clean up k6 job %s in namespace %s", jobName, namespace)
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

// waitForStatefulSetReplicas polls until the StatefulSet has the expected replica
// count and at least that many pods exist.
func waitForStatefulSetReplicas(namespace, clusterName string, expectedReplicas int32) {
	Expect(framework.WaitForScaleAck(ctx, k8sClientset, namespace, clusterName, expectedReplicas, scaleAckTimeout, scalePollInterval)).To(Succeed(),
		fmt.Sprintf("StatefulSet should have %d replicas with pods", expectedReplicas))
}

// ---------------------------------------------------------------------------
// Shared chaos scenario bodies
// ---------------------------------------------------------------------------
// Each function runs the full chaos scenario (start k6, chaos loop, verify)
// and returns the k6 job name so the caller can clean it up.

// runScalingChaos runs the continuous-scaling-and-pod-deletion scenario.
func runScalingChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load job")
	k6JobName := startK6OrFail(namespace, clusterName, chaosDuration, defaultVUs)

	By("executing chaos loop")
	endTime := time.Now().Add(chaosDuration - chaosReserveTime)

	iteration := 0
	for time.Now().Before(endTime) {
		iteration++
		GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

		By(fmt.Sprintf("iteration %d: scaling cluster up", iteration))
		newSize := int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
		Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, newSize)).To(Succeed())

		Expect(framework.WaitForScaleAck(ctx, k8sClientset, namespace, clusterName, newSize, scaleAckTimeout, scalePollInterval)).To(Succeed())

		By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
		deleteCount := rng.Intn(int(newSize)/2) + 1
		deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, deleteCount, rng)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")
		GinkgoWriter.Printf("Deleted pods: %v\n", deleted)

		By(fmt.Sprintf("iteration %d: waiting for cluster recovery", iteration))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

		By(fmt.Sprintf("iteration %d: scaling cluster down", iteration))
		downSize := int32(rng.Intn(3) + minPrimaries)
		Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, downSize)).To(Succeed())

		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())
	}

	By("verifying final cluster state")
	verifyClusterHealthy(namespace, clusterName)
	verifyK6Completed(namespace, k6JobName, chaosDuration+k6CompletionBuffer)

	return k6JobName
}

// runOperatorDeletionChaos runs the operator-pod-deletion scenario.
func runOperatorDeletionChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load job")
	k6JobName := startK6OrFail(namespace, clusterName, chaosDuration, defaultVUs)

	By("executing chaos with operator deletion")
	endTime := time.Now().Add(chaosDuration - chaosReserveTime)

	iteration := 0
	for time.Now().Before(endTime) {
		iteration++
		GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

		By(fmt.Sprintf("iteration %d: deleting operator pod", iteration))
		Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace)).To(Succeed())

		By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
		deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")

		By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

		// Rate limit between iterations
		time.Sleep(chaosRateLimitDelay)
	}

	By("verifying final cluster state")
	Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())
	verifyK6Completed(namespace, k6JobName, chaosDuration+k6CompletionBuffer)

	return k6JobName
}

// runRobinDeletionChaos runs the robin-pod-deletion scenario.
func runRobinDeletionChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load job")
	k6JobName := startK6OrFail(namespace, clusterName, chaosDuration, defaultVUs)

	By("executing chaos with robin deletion")
	endTime := time.Now().Add(chaosDuration - chaosReserveTime)

	iteration := 0
	for time.Now().Before(endTime) {
		iteration++
		GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

		By(fmt.Sprintf("iteration %d: deleting robin pods", iteration))
		Expect(framework.DeleteRobinPods(ctx, k8sClientset, namespace, clusterName)).To(Succeed())

		By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
		deletedRedis, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedRedis).NotTo(BeEmpty(), "expected at least one redis pod deletion")

		By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

		// Rate limit between iterations
		time.Sleep(chaosRateLimitDelay)
	}

	By("verifying final cluster state")
	Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())
	verifyK6Completed(namespace, k6JobName, chaosDuration+k6CompletionBuffer)

	return k6JobName
}

// runFullChaos runs the full chaos scenario with random operator, robin, redis,
// and scaling actions.
func runFullChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load job")
	k6JobName := startK6OrFail(namespace, clusterName, chaosDuration, defaultVUs)

	By("executing full chaos")
	endTime := time.Now().Add(chaosDuration - chaosReserveTime)

	iteration := 0
	for time.Now().Before(endTime) {
		iteration++
		GinkgoWriter.Printf("=== Full chaos iteration %d ===\n", iteration)

		action := rng.Intn(4)

		switch action {
		case 0:
			By(fmt.Sprintf("iteration %d: deleting operator pod", iteration))
			Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace)).To(Succeed())
		case 1:
			By(fmt.Sprintf("iteration %d: deleting robin pods", iteration))
			Expect(framework.DeleteRobinPods(ctx, k8sClientset, namespace, clusterName)).To(Succeed())
		case 2:
			By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
			deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")
		case 3:
			By(fmt.Sprintf("iteration %d: scaling cluster", iteration))
			newSize := int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
			Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, newSize)).To(Succeed())
		}

		By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

		// Rate limit between chaos actions
		time.Sleep(chaosIterationDelay)
	}

	By("verifying final cluster state")
	verifyClusterHealthy(namespace, clusterName)
	verifyK6Completed(namespace, k6JobName, chaosDuration+k6CompletionBuffer)

	return k6JobName
}
