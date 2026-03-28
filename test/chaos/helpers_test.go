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

// startK6OrFail starts a k6 load deployment and fails the test if it errors.
func startK6OrFail(namespace, clusterName string, vus int) string {
	depName, err := framework.StartK6LoadDeployment(ctx, k8sClientset, namespace, clusterName, vus)
	Expect(err).NotTo(HaveOccurred(), "failed to start k6 load deployment")
	return depName
}

// stopK6Load stops the k6 load deployment and fails the spec if cleanup fails.
func stopK6Load(namespace, depName string) {
	if depName == "" {
		return
	}
	Expect(framework.StopK6Load(ctx, k8sClientset, namespace, depName)).To(Succeed(), "failed to stop k6 deployment %s in namespace %s", depName, namespace)
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

// ---------------------------------------------------------------------------
// Shared chaos scenario bodies
// ---------------------------------------------------------------------------
// Each function runs the full chaos scenario (start k6, chaos loop, verify)
// and returns the k6 deployment name so the caller can clean it up.

// runScalingChaos runs the continuous-scaling-and-pod-deletion scenario.
// When purgeKeysOnRebalance is true the operator deletes and recreates the
// StatefulSet on scaling, so we must wait for the new StatefulSet to
// acknowledge the target replica count before proceeding. When false the
// StatefulSet is updated in place and the acknowledgment step is skipped.
func runScalingChaos(rng *rand.Rand, namespace, clusterName string, purgeKeysOnRebalance bool) string {
	By("starting k6 load deployment")
	k6DepName := startK6OrFail(namespace, clusterName, defaultVUs)

	By(fmt.Sprintf("executing chaos loop (%d iterations)", chaosIterations))

	currentPrimaries := int32(defaultPrimaries)

	for i := 1; i <= chaosIterations; i++ {
		GinkgoWriter.Printf("=== Chaos iteration %d/%d ===\n", i, chaosIterations)

		By(fmt.Sprintf("iteration %d/%d: scaling cluster up", i, chaosIterations))
		newSize := int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
		GinkgoWriter.Printf("Scaling up: %d -> %d primaries\n", currentPrimaries, newSize)
		Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, newSize)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: failed to scale cluster up to %d", i, chaosIterations, newSize))

		// When purge is enabled the operator deletes and recreates the
		// StatefulSet, so we must wait for the new one to appear with the
		// correct replica count before interacting with pods.
		if purgeKeysOnRebalance {
			Expect(framework.WaitForScaleAck(ctx, k8sClientset, namespace, clusterName, newSize, scaleAckTimeout, scalePollInterval)).To(Succeed(),
				fmt.Sprintf("iteration %d/%d: StatefulSet did not acknowledge scale to %d", i, chaosIterations, newSize))
		}

		By(fmt.Sprintf("iteration %d/%d: deleting random redis pods", i, chaosIterations))
		deleteCount := rng.Intn(int(newSize)/2) + 1
		deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, deleteCount, rng)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to delete random redis pods", i, chaosIterations))
		Expect(deleted).NotTo(BeEmpty(), fmt.Sprintf("iteration %d/%d: expected at least one redis pod deletion", i, chaosIterations))
		GinkgoWriter.Printf("Deleted pods: %v\n", deleted)

		By(fmt.Sprintf("iteration %d/%d: waiting for cluster recovery", i, chaosIterations))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: cluster did not recover after pod deletion", i, chaosIterations))

		// Verify the cluster spec matches what we scaled to.
		cluster, err := framework.GetRedkeyCluster(ctx, dynamicClient, namespace, clusterName)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to get cluster after scale-up recovery", i, chaosIterations))
		Expect(cluster.Spec.Primaries).To(Equal(newSize),
			fmt.Sprintf("iteration %d/%d: expected spec.primaries=%d after scale-up, got %d", i, chaosIterations, newSize, cluster.Spec.Primaries))
		currentPrimaries = newSize

		By(fmt.Sprintf("iteration %d/%d: scaling cluster down", i, chaosIterations))
		downSize := int32(minPrimaries - rng.Intn(3))
		GinkgoWriter.Printf("Scaling down: %d -> %d primaries\n", currentPrimaries, downSize)
		Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, downSize)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: failed to scale cluster down to %d", i, chaosIterations, downSize))

		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: cluster did not become ready after scaling down", i, chaosIterations))

		// Verify the cluster spec matches what we scaled to.
		cluster, err = framework.GetRedkeyCluster(ctx, dynamicClient, namespace, clusterName)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to get cluster after scale-down recovery", i, chaosIterations))
		Expect(cluster.Spec.Primaries).To(Equal(downSize),
			fmt.Sprintf("iteration %d/%d: expected spec.primaries=%d after scale-down, got %d", i, chaosIterations, downSize, cluster.Spec.Primaries))
		currentPrimaries = downSize
	}

	By("stopping k6 load")
	stopK6Load(namespace, k6DepName)

	By("verifying final cluster state")
	verifyClusterHealthy(namespace, clusterName)

	return k6DepName
}

// runOperatorDeletionChaos runs the operator-pod-deletion scenario.
func runOperatorDeletionChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load deployment")
	k6DepName := startK6OrFail(namespace, clusterName, defaultVUs)

	By(fmt.Sprintf("executing chaos with operator deletion (%d iterations)", chaosIterations))

	for i := 1; i <= chaosIterations; i++ {
		GinkgoWriter.Printf("=== Chaos iteration %d/%d ===\n", i, chaosIterations)

		By(fmt.Sprintf("iteration %d/%d: deleting operator pod", i, chaosIterations))
		Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: failed to delete operator pods", i, chaosIterations))

		By(fmt.Sprintf("iteration %d/%d: deleting random redis pods", i, chaosIterations))
		deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to delete random redis pods", i, chaosIterations))
		Expect(deleted).NotTo(BeEmpty(), fmt.Sprintf("iteration %d/%d: expected at least one redis pod deletion", i, chaosIterations))
		GinkgoWriter.Printf("Deleted pods: %v\n", deleted)

		By(fmt.Sprintf("iteration %d/%d: waiting for recovery", i, chaosIterations))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: cluster did not recover after operator deletion", i, chaosIterations))

		// Rate limit between iterations
		time.Sleep(chaosRateLimitDelay)
	}

	By("stopping k6 load")
	stopK6Load(namespace, k6DepName)

	By("verifying final cluster state")
	Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

	return k6DepName
}

// runRobinDeletionChaos runs the robin-pod-deletion scenario.
func runRobinDeletionChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load deployment")
	k6DepName := startK6OrFail(namespace, clusterName, defaultVUs)

	By(fmt.Sprintf("executing chaos with robin deletion (%d iterations)", chaosIterations))

	for i := 1; i <= chaosIterations; i++ {
		GinkgoWriter.Printf("=== Chaos iteration %d/%d ===\n", i, chaosIterations)

		By(fmt.Sprintf("iteration %d/%d: deleting robin pods", i, chaosIterations))
		Expect(framework.DeleteRobinPods(ctx, k8sClientset, namespace, clusterName)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: failed to delete robin pods", i, chaosIterations))

		By(fmt.Sprintf("iteration %d/%d: deleting random redis pods", i, chaosIterations))
		deletedRedis, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to delete random redis pods", i, chaosIterations))
		Expect(deletedRedis).NotTo(BeEmpty(), fmt.Sprintf("iteration %d/%d: expected at least one redis pod deletion", i, chaosIterations))
		GinkgoWriter.Printf("Deleted pods: %v\n", deletedRedis)

		By(fmt.Sprintf("iteration %d/%d: waiting for recovery", i, chaosIterations))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: cluster did not recover after robin deletion", i, chaosIterations))

		// Rate limit between iterations
		time.Sleep(chaosRateLimitDelay)
	}

	By("stopping k6 load")
	stopK6Load(namespace, k6DepName)

	By("verifying final cluster state")
	Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed())

	return k6DepName
}

// runFullChaos runs the full chaos scenario firing multiple actions per
// iteration — operator deletion, robin deletion, redis pod deletion, and
// scaling — without waiting for recovery between them. This tests the
// operator's ability to heal from accumulated, overlapping failures.
func runFullChaos(rng *rand.Rand, namespace, clusterName string) string {
	By("starting k6 load deployment")
	k6DepName := startK6OrFail(namespace, clusterName, defaultVUs)

	By(fmt.Sprintf("executing full chaos (%d iterations)", chaosIterations))

	currentPrimaries := int32(defaultPrimaries)
	var scaled bool

	for i := 1; i <= chaosIterations; i++ {
		GinkgoWriter.Printf("=== Full chaos iteration %d/%d ===\n", i, chaosIterations)
		scaled = false

		// Each action is independently chosen so multiple (or all) can fire
		// in the same iteration, accumulating failures before recovery.

		if rng.Intn(2) == 0 {
			By(fmt.Sprintf("iteration %d/%d: deleting operator pod", i, chaosIterations))
			Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace)).To(Succeed(),
				fmt.Sprintf("iteration %d/%d: failed to delete operator pods", i, chaosIterations))
		}

		if rng.Intn(2) == 0 {
			By(fmt.Sprintf("iteration %d/%d: deleting robin pods", i, chaosIterations))
			Expect(framework.DeleteRobinPods(ctx, k8sClientset, namespace, clusterName)).To(Succeed(),
				fmt.Sprintf("iteration %d/%d: failed to delete robin pods", i, chaosIterations))
		}

		if rng.Intn(2) == 0 {
			By(fmt.Sprintf("iteration %d/%d: deleting random redis pods", i, chaosIterations))
			deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace, clusterName, 2, rng)
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to delete random redis pods", i, chaosIterations))
			Expect(deleted).NotTo(BeEmpty(), fmt.Sprintf("iteration %d/%d: expected at least one redis pod deletion", i, chaosIterations))
			GinkgoWriter.Printf("Deleted pods: %v\n", deleted)
		}

		var newSize int32
		if rng.Intn(2) == 0 {
			By(fmt.Sprintf("iteration %d/%d: scaling cluster", i, chaosIterations))
			newSize = int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
			GinkgoWriter.Printf("Scaling: %d -> %d primaries\n", currentPrimaries, newSize)
			Expect(framework.ScaleCluster(ctx, dynamicClient, namespace, clusterName, newSize)).To(Succeed(),
				fmt.Sprintf("iteration %d/%d: failed to scale cluster to %d", i, chaosIterations, newSize))
			scaled = true
		}

		By(fmt.Sprintf("iteration %d/%d: waiting for recovery", i, chaosIterations))
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace, clusterName, chaosReadyTimeout)).To(Succeed(),
			fmt.Sprintf("iteration %d/%d: cluster did not recover after chaos actions", i, chaosIterations))

		// Verify the cluster spec matches what we expect after recovery.
		if scaled {
			cluster, err := framework.GetRedkeyCluster(ctx, dynamicClient, namespace, clusterName)
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("iteration %d/%d: failed to get cluster after recovery", i, chaosIterations))
			Expect(cluster.Spec.Primaries).To(Equal(newSize),
				fmt.Sprintf("iteration %d/%d: expected spec.primaries=%d after scaling, got %d", i, chaosIterations, newSize, cluster.Spec.Primaries))
			currentPrimaries = newSize
		}

		// Rate limit between chaos iterations
		time.Sleep(chaosIterationDelay)
	}

	By("stopping k6 load")
	stopK6Load(namespace, k6DepName)

	By("verifying final cluster state")
	verifyClusterHealthy(namespace, clusterName)

	return k6DepName
}
