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

var _ = Describe("Chaos Under Load (PurgeKeysOnRebalance=true)", Label("chaos", "load"), func() {
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
		Expect(framework.CreateRedkeyCluster(ctx, dynamicClient, namespace.Name, clusterName, defaultPrimaries, true)).To(Succeed())

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
		if skipDeleteNamespace && CurrentSpecReport().Failed() {
			GinkgoWriter.Printf("CHAOS_SKIP_DELETE_NAMESPACE is set and spec failed — preserving namespace %s for inspection\n", namespaceName)
		} else {
			Expect(framework.DeleteNamespace(ctx, k8sClientset, dynamicClient, namespace)).To(Succeed(), "failed to clean up namespace %s", namespaceName)
		}
	})

	// ==================================================================================
	// Scenario 1: Continuous Scaling Under Load and Chaos (PurgeKeysOnRebalance=true)
	//              PurgeKeysOnRebalance=true --> the StatefulSet is recreated when scaling
	// ==================================================================================
	It("survives continuous scaling and pod deletion while handling traffic", func() {
		k6JobName = runScalingChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 2: Chaos with Operator Deletion
	// ==================================================================================
	It("recovers when operator pod is deleted during chaos", func() {
		k6JobName = runOperatorDeletionChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 3: Chaos with Robin Deletion
	// ==================================================================================
	It("recovers when robin pods are deleted during chaos", func() {
		k6JobName = runRobinDeletionChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 4: Full Chaos (Operator + Robin + Redis)
	// ==================================================================================
	It("recovers from full chaos deleting operator, robin, and redis pods", func() {
		k6JobName = runFullChaos(rng, namespace.Name, clusterName)
	})
})

// ======================================================================================
// Chaos Under Load (NoPurge) — same scenarios with PurgeKeysOnRebalance=false
// PurgeKeysOnRebalance=false --> the StatefulSet is updated in place when scaling
// ======================================================================================
var _ = Describe("Chaos Under Load (PurgeKeysOnRebalance=false)", Label("chaos", "load", "nopurge"), func() {
	var (
		namespace *corev1.Namespace
		k6JobName string
		rng       *rand.Rand
	)

	BeforeEach(func() {
		var err error

		rng = rand.New(rand.NewSource(chaosSeed))
		GinkgoWriter.Printf("Using random seed: %d\n", chaosSeed)

		namespace, err = framework.CreateNamespace(ctx, k8sClientset, fmt.Sprintf("chaos-np-%d", GinkgoParallelProcess()))
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace")

		By("deploying operator in namespace")
		Expect(framework.EnsureOperatorSetup(ctx, k8sClientset, namespace.Name)).To(Succeed())

		Eventually(func() bool {
			dep, err := k8sClientset.AppsV1().Deployments(namespace.Name).Get(ctx, "redkey-operator", metav1.GetOptions{})
			return err == nil && dep.Status.AvailableReplicas >= 1
		}, operatorReadyTimeout, operatorPollInterval).Should(BeTrue())

		By("creating Redis cluster with 5 primaries (PurgeKeysOnRebalance=false)")
		Expect(framework.CreateRedkeyCluster(ctx, dynamicClient, namespace.Name, clusterName, defaultPrimaries, false)).To(Succeed())

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
		if skipDeleteNamespace && CurrentSpecReport().Failed() {
			GinkgoWriter.Printf("CHAOS_SKIP_DELETE_NAMESPACE is set and spec failed — preserving namespace %s for inspection\n", namespaceName)
		} else {
			Expect(framework.DeleteNamespace(ctx, k8sClientset, dynamicClient, namespace)).To(Succeed(), "failed to clean up namespace %s", namespaceName)
		}
	})

	// ==================================================================================
	// Scenario 1 (NoPurge): Continuous Scaling Under Load and Chaos
	// ==================================================================================
	It("survives continuous scaling and pod deletion while handling traffic without purge", func() {
		k6JobName = runScalingChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 2 (NoPurge): Chaos with Operator Deletion
	// ==================================================================================
	It("recovers when operator pod is deleted during chaos without purge", func() {
		k6JobName = runOperatorDeletionChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 3 (NoPurge): Chaos with Robin Deletion
	// ==================================================================================
	It("recovers when robin pods are deleted during chaos without purge", func() {
		k6JobName = runRobinDeletionChaos(rng, namespace.Name, clusterName)
	})

	// ==================================================================================
	// Scenario 4 (NoPurge): Full Chaos (Operator + Robin + Redis)
	// ==================================================================================
	It("recovers from full chaos deleting operator, robin, and redis pods without purge", func() {
		k6JobName = runFullChaos(rng, namespace.Name, clusterName)
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
		Expect(framework.CreateRedkeyCluster(ctx, dynamicClient, namespace.Name, clusterName, defaultPrimaries, true)).To(Succeed())

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
		if skipDeleteNamespace && CurrentSpecReport().Failed() {
			GinkgoWriter.Printf("CHAOS_SKIP_DELETE_NAMESPACE is set and spec failed — preserving namespace %s for inspection\n", namespaceName)
		} else {
			Expect(framework.DeleteNamespace(ctx, k8sClientset, dynamicClient, namespace)).To(Succeed(), "failed to clean up namespace %s", namespaceName)
		}
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
