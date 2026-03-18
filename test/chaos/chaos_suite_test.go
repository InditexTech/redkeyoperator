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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	clusterName      = "redis-cluster"
	defaultPrimaries = 5

	// Chaos timing constants
	chaosIterationDelay  = 5 * time.Second  // Delay between chaos iterations
	chaosRateLimitDelay  = 10 * time.Second // Delay for rate limiting between heavy operations
	chaosReserveTime     = 1 * time.Minute  // Time reserved at end of chaos for final checks
	k6CompletionBuffer   = 5 * time.Minute  // Buffer time for k6 job completion
	operatorReadyTimeout = 2 * time.Minute  // Timeout for operator to become ready
	operatorPollInterval = 5 * time.Second  // Poll interval for operator readiness
	scaleAckTimeout      = 30 * time.Second // Timeout for StatefulSet to acknowledge scale
	scalePollInterval    = 2 * time.Second  // Poll interval for scale acknowledgment
	diagnosticsLogTail   = int64(100)       // Number of log lines to capture for diagnostics

	// Scaling bounds
	minPrimaries = 3
	maxPrimaries = 10
	defaultVUs   = 10 // Number of virtual users for k6 load tests
)

var _ = Describe("Chaos Under Load", Label("chaos", "load"), func() {
	var (
		namespace *corev1.Namespace
		k6JobName string
		rng       *rand.Rand
	)

	BeforeEach(func() {
		var err error

		rng = rand.New(rand.NewSource(chaosSeed))
		GinkgoWriter.Printf("Using random seed: %d\n", chaosSeed)

		namespace, err = framework.CreateNamespace(ctx, k8sClientset, fmt.Sprintf("chaos-%d", GinkgoParallelProcess()))
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace")

		By("deploying operator in namespace")
		Expect(framework.EnsureOperatorSetup(ctx, k8sClientset, namespace.Name)).To(Succeed())

		Eventually(func() bool {
			dep, err := k8sClientset.AppsV1().Deployments(namespace.Name).Get(ctx, "redkey-operator", metav1.GetOptions{})
			return err == nil && dep.Status.AvailableReplicas >= 1
		}, operatorReadyTimeout, operatorPollInterval).Should(BeTrue())

		By("creating Redis cluster with 5 primaries")
		Expect(framework.CreateRedkeyCluster(ctx, dynamicClient, namespace.Name, clusterName, defaultPrimaries)).To(Succeed())

		By("waiting for cluster to be ready")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
	})

	AfterEach(func() {
		namespaceName := ""
		if namespace != nil {
			namespaceName = namespace.Name
		}

		if CurrentSpecReport().Failed() && namespaceName != "" {
			collectDiagnostics(namespace.Name)
		}
		if k6JobName != "" {
			Expect(namespaceName).NotTo(BeEmpty(), "k6 job cleanup requires a namespace")
			Expect(framework.DeleteK6Job(ctx, k8sClientset, namespaceName, k6JobName)).To(Succeed(), "failed to clean up k6 job %s in namespace %s", k6JobName, namespaceName)
		}
		Expect(framework.DeleteNamespace(ctx, k8sClientset, dynamicClient, namespace)).To(Succeed(), "failed to clean up namespace %s", namespaceName)
	})

	// ==================================================================================
	// Scenario 1: Continuous Scaling Under Load and Chaos (PurgeKeysOnRebalance=true)
	//              PurgeKeysOnRebalance=true --> the StatefulSet is recreated when scaling
	// ==================================================================================
	It("survives continuous scaling and pod deletion while handling traffic", func() {
		By("starting k6 load job")
		var err error
		k6JobName, err = framework.StartK6LoadJob(ctx, k8sClientset, namespace.Name, clusterName, chaosDuration, defaultVUs)
		Expect(err).NotTo(HaveOccurred())

		By("executing chaos loop")
		endTime := time.Now().Add(chaosDuration - chaosReserveTime)

		iteration := 0
		for time.Now().Before(endTime) {
			iteration++
			GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

			By(fmt.Sprintf("iteration %d: scaling cluster up", iteration))
			newSize := int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
			Expect(framework.ScaleCluster(ctx, dynamicClient, namespace.Name, clusterName, newSize)).To(Succeed())

			// Poll for StatefulSet to acknowledge the scale and pods to exist.
			// During fast scaling (PurgeKeysOnRebalance=true), the operator deletes and
			// recreates the StatefulSet, so we must wait for pods to actually exist
			// before attempting to delete them.
			Eventually(func() int {
				pods, err := k8sClientset.CoreV1().Pods(namespace.Name).List(ctx, metav1.ListOptions{
					LabelSelector: framework.RedisPodsSelector(clusterName),
				})
				if err != nil {
					return 0
				}
				return len(pods.Items)
			}, scaleAckTimeout, scalePollInterval).Should(BeNumerically(">=", int(newSize)))

			By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
			deleteCount := rng.Intn(int(newSize)/2) + 1
			deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace.Name, clusterName, deleteCount, rng)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")
			GinkgoWriter.Printf("Deleted pods: %v\n", deleted)

			By(fmt.Sprintf("iteration %d: waiting for cluster recovery", iteration))
			Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

			By(fmt.Sprintf("iteration %d: scaling cluster down", iteration))
			downSize := int32(rng.Intn(3) + minPrimaries)
			Expect(framework.ScaleCluster(ctx, dynamicClient, namespace.Name, clusterName, downSize)).To(Succeed())

			Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		}

		By("verifying final cluster state")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("verifying k6 job completed successfully")
		Expect(framework.WaitForK6JobCompletion(ctx, k8sClientset, namespace.Name, k6JobName, chaosDuration+k6CompletionBuffer)).To(Succeed())
	})

	// ==================================================================================
	// Scenario 2: Chaos with Operator Deletion
	// ==================================================================================
	It("recovers when operator pod is deleted during chaos", func() {
		By("starting k6 load job")
		var err error
		k6JobName, err = framework.StartK6LoadJob(ctx, k8sClientset, namespace.Name, clusterName, chaosDuration, defaultVUs)
		Expect(err).NotTo(HaveOccurred())

		By("executing chaos with operator deletion")
		endTime := time.Now().Add(chaosDuration - chaosReserveTime)

		iteration := 0
		for time.Now().Before(endTime) {
			iteration++
			GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

			By(fmt.Sprintf("iteration %d: deleting operator pod", iteration))
			Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace.Name)).To(Succeed())

			By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
			deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace.Name, clusterName, 2, rng)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")

			By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
			Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

			// Rate limit between iterations
			time.Sleep(chaosRateLimitDelay)
		}

		By("verifying final cluster state")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.WaitForK6JobCompletion(ctx, k8sClientset, namespace.Name, k6JobName, chaosDuration+k6CompletionBuffer)).To(Succeed())
	})

	// ==================================================================================
	// Scenario 3: Chaos with Robin Deletion
	// ==================================================================================
	It("recovers when robin pods are deleted during chaos", func() {
		By("starting k6 load job")
		var err error
		k6JobName, err = framework.StartK6LoadJob(ctx, k8sClientset, namespace.Name, clusterName, chaosDuration, defaultVUs)
		Expect(err).NotTo(HaveOccurred())

		By("executing chaos with robin deletion")
		endTime := time.Now().Add(chaosDuration - chaosReserveTime)

		iteration := 0
		for time.Now().Before(endTime) {
			iteration++
			GinkgoWriter.Printf("=== Chaos iteration %d ===\n", iteration)

			By(fmt.Sprintf("iteration %d: deleting robin pods", iteration))
			deletedRobin, err := framework.DeleteRobinPods(ctx, k8sClientset, namespace.Name, clusterName, 2, rng)
			Expect(err).NotTo(HaveOccurred())
			Expect(deletedRobin).NotTo(BeEmpty(), "expected at least one robin pod deletion")

			By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
			deletedRedis, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace.Name, clusterName, 2, rng)
			Expect(err).NotTo(HaveOccurred())
			Expect(deletedRedis).NotTo(BeEmpty(), "expected at least one redis pod deletion")

			By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
			Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

			// Rate limit between iterations
			time.Sleep(chaosRateLimitDelay)
		}

		By("verifying final cluster state")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.WaitForK6JobCompletion(ctx, k8sClientset, namespace.Name, k6JobName, chaosDuration+k6CompletionBuffer)).To(Succeed())
	})

	// ==================================================================================
	// Scenario 4: Full Chaos (Operator + Robin + Redis)
	// ==================================================================================
	It("recovers from full chaos deleting operator, robin, and redis pods", func() {
		By("starting k6 load job")
		var err error
		k6JobName, err = framework.StartK6LoadJob(ctx, k8sClientset, namespace.Name, clusterName, chaosDuration, defaultVUs)
		Expect(err).NotTo(HaveOccurred())

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
				Expect(framework.DeleteOperatorPods(ctx, k8sClientset, namespace.Name)).To(Succeed())
			case 1:
				By(fmt.Sprintf("iteration %d: deleting robin pods", iteration))
				deleted, err := framework.DeleteRobinPods(ctx, k8sClientset, namespace.Name, clusterName, 2, rng)
				Expect(err).NotTo(HaveOccurred())
				Expect(deleted).NotTo(BeEmpty(), "expected at least one robin pod deletion")
			case 2:
				By(fmt.Sprintf("iteration %d: deleting random redis pods", iteration))
				deleted, err := framework.DeleteRandomRedisPods(ctx, k8sClientset, namespace.Name, clusterName, 2, rng)
				Expect(err).NotTo(HaveOccurred())
				Expect(deleted).NotTo(BeEmpty(), "expected at least one redis pod deletion")
			case 3:
				By(fmt.Sprintf("iteration %d: scaling cluster", iteration))
				newSize := int32(rng.Intn(maxPrimaries-minPrimaries+1) + minPrimaries)
				Expect(framework.ScaleCluster(ctx, dynamicClient, namespace.Name, clusterName, newSize)).To(Succeed())
			}

			By(fmt.Sprintf("iteration %d: waiting for recovery", iteration))
			Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

			// Rate limit between chaos actions
			time.Sleep(chaosIterationDelay)
		}

		By("verifying final cluster state")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.WaitForK6JobCompletion(ctx, k8sClientset, namespace.Name, k6JobName, chaosDuration+k6CompletionBuffer)).To(Succeed())
	})
})

var _ = Describe("Topology Corruption Recovery", Label("chaos", "topology"), func() {
	var (
		namespace *corev1.Namespace
	)

	BeforeEach(func() {
		var err error

		namespace, err = framework.CreateNamespace(ctx, k8sClientset, fmt.Sprintf("chaos-topo-%d", GinkgoParallelProcess()))
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace")

		By("deploying operator in namespace")
		Expect(framework.EnsureOperatorSetup(ctx, k8sClientset, namespace.Name)).To(Succeed())

		Eventually(func() bool {
			dep, err := k8sClientset.AppsV1().Deployments(namespace.Name).Get(ctx, "redkey-operator", metav1.GetOptions{})
			return err == nil && dep.Status.AvailableReplicas >= 1
		}, operatorReadyTimeout, operatorPollInterval).Should(BeTrue())

		By("creating Redis cluster with 5 primaries")
		Expect(framework.CreateRedkeyCluster(ctx, dynamicClient, namespace.Name, clusterName, defaultPrimaries)).To(Succeed())

		By("waiting for cluster to be ready")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
	})

	AfterEach(func() {
		namespaceName := ""
		if namespace != nil {
			namespaceName = namespace.Name
		}

		if CurrentSpecReport().Failed() && namespaceName != "" {
			collectDiagnostics(namespace.Name)
		}
		Expect(framework.DeleteNamespace(ctx, k8sClientset, dynamicClient, namespace)).To(Succeed(), "failed to clean up namespace %s", namespaceName)
	})

	// ==================================================================================
	// Scenario 5: Slot Ownership Conflict Recovery
	// ==================================================================================
	It("heals slot ownership conflicts when operator and robin restart", func() {
		By("verifying cluster is ready")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

		By("scaling operator to 0")
		Expect(framework.ScaleOperatorDown(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("scaling robin to 0")
		Expect(framework.ScaleRobinDown(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("corrupting slot ownership via redis-cli")
		Expect(framework.CorruptSlotOwnership(ctx, k8sClientset, namespace.Name, clusterName, 0)).To(Succeed())

		By("scaling robin to 1")
		Expect(framework.ScaleRobinUp(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("scaling operator to 1")
		Expect(framework.ScaleOperatorUp(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("waiting for cluster to heal")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
	})

	// ==================================================================================
	// Scenario 6: Mid-Migration Slot Recovery
	// ==================================================================================
	It("recovers from mid-migration slots when operator and robin restart", func() {
		By("verifying cluster is ready")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

		By("scaling operator to 0")
		Expect(framework.ScaleOperatorDown(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("scaling robin to 0")
		Expect(framework.ScaleRobinDown(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("setting slot to migrating state via redis-cli")
		Expect(framework.SetSlotMigrating(ctx, k8sClientset, namespace.Name, clusterName, 100)).To(Succeed())

		By("scaling robin to 1")
		Expect(framework.ScaleRobinUp(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("scaling operator to 1")
		Expect(framework.ScaleOperatorUp(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("waiting for cluster to heal")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
	})

	// ==================================================================================
	// Scenario 7: Primary to Replica Demotion Recovery
	// ==================================================================================
	It("recovers from forced primary to replica demotion", func() {
		By("verifying cluster is ready")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())

		targetPod := clusterName + "-0"

		By("scaling operator to 0")
		Expect(framework.ScaleOperatorDown(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("scaling robin to 0")
		Expect(framework.ScaleRobinDown(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("forcing primary to become replica via redis-cli")
		Expect(framework.ForcePrimaryToReplica(ctx, k8sClientset, namespace.Name, clusterName, targetPod)).To(Succeed())

		By("scaling robin to 1")
		Expect(framework.ScaleRobinUp(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())

		By("scaling operator to 1")
		Expect(framework.ScaleOperatorUp(ctx, k8sClientset, namespace.Name)).To(Succeed())

		By("waiting for cluster to heal")
		Expect(framework.WaitForChaosReady(ctx, dynamicClient, k8sClientset, namespace.Name, clusterName, chaosReadyTimeout)).To(Succeed())
		Expect(framework.AssertAllSlotsAssigned(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
		Expect(framework.AssertNoNodesInFailState(ctx, k8sClientset, namespace.Name, clusterName)).To(Succeed())
	})
})

// collectDiagnostics collects logs and state for debugging failed tests.
func collectDiagnostics(namespace string) {
	GinkgoWriter.Printf("\n=== COLLECTING DIAGNOSTICS FOR NAMESPACE %s ===\n", namespace)

	// Get cluster status
	cluster, err := framework.GetRedkeyCluster(ctx, dynamicClient, namespace, clusterName)
	if err == nil {
		GinkgoWriter.Printf("Cluster status: %s\n", cluster.Status.Status)
		GinkgoWriter.Printf("Cluster conditions: %+v\n", cluster.Status.Conditions)
	}

	// List pods with status
	pods, err := k8sClientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		GinkgoWriter.Printf("\nPods in namespace:\n")
		for _, pod := range pods.Items {
			GinkgoWriter.Printf("  %s: Phase=%s\n", pod.Name, pod.Status.Phase)
		}
	}

	// Capture operator pod logs
	GinkgoWriter.Printf("\n--- Operator Pod Logs (last %d lines) ---\n", diagnosticsLogTail)
	operatorLogs, err := framework.GetPodLogs(ctx, k8sClientset, namespace, framework.OperatorPodsSelector(), diagnosticsLogTail)
	if err == nil {
		GinkgoWriter.Printf("%s\n", operatorLogs)
	} else {
		GinkgoWriter.Printf("Failed to get operator logs: %v\n", err)
	}

	// Capture first redis pod logs
	GinkgoWriter.Printf("\n--- Redis Pod Logs (last %d lines, first pod) ---\n", diagnosticsLogTail)
	redisLogs, err := framework.GetPodLogs(ctx, k8sClientset, namespace, framework.RedisPodsSelector(clusterName), diagnosticsLogTail)
	if err == nil {
		GinkgoWriter.Printf("%s\n", redisLogs)
	} else {
		GinkgoWriter.Printf("Failed to get redis logs: %v\n", err)
	}

	GinkgoWriter.Printf("=== END DIAGNOSTICS ===\n\n")
}
