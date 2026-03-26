// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	k8sClientset        kubernetes.Interface
	dynamicClient       dynamic.Interface
	ctx                 context.Context
	cancel              context.CancelFunc
	chaosIterations     int
	chaosSeed           int64
	chaosReadyTimeout   = 10 * time.Minute
	skipDeleteNamespace bool
)

func TestChaos(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Redkey Operator Chaos Test Suite", Label("chaos"))
}

// SynchronizedBeforeSuite ensures cluster-level setup runs once across all
// parallel Ginkgo processes. The first process (process 1) performs the
// one-time setup, and all processes then create their own Kubernetes client.
var _ = SynchronizedBeforeSuite(
	func() []byte {
		By("verifying CRD directory exists (process 1)")
		crdDir := filepath.Join("..", "..", "deployment")
		_, err := os.Stat(crdDir)
		Expect(err).NotTo(HaveOccurred(), "CRD directory %q must exist", crdDir)

		return nil
	},
	func(_ []byte) {
		By("creating Kubernetes clients")

		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}

		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		Expect(err).NotTo(HaveOccurred(), "failed to load kubeconfig from %s", kubeconfig)
		Expect(cfg).NotTo(BeNil())

		// Create native kubernetes clientset
		k8sClientset, err = kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred(), "failed to create Kubernetes clientset")
		Expect(k8sClientset).NotTo(BeNil())

		// Create dynamic client for CRD access
		dynamicClient, err = dynamic.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred(), "failed to create dynamic client")
		Expect(dynamicClient).NotTo(BeNil())

		ctx, cancel = context.WithCancel(context.Background())

		chaosIterations = parseInt(os.Getenv("CHAOS_ITERATIONS"), 3)

		if seedStr := os.Getenv("CHAOS_SEED"); seedStr != "" {
			seed, err := strconv.ParseInt(seedStr, 10, 64)
			if err == nil {
				chaosSeed = seed
			} else {
				chaosSeed = GinkgoRandomSeed()
			}
		} else {
			chaosSeed = GinkgoRandomSeed()
		}

		if os.Getenv("CHAOS_KEEP_NAMESPACE_ON_FAILED") != "" {
			skipDeleteNamespace = true
		}

		GinkgoWriter.Printf("Chaos test configuration: iterations=%d, seed=%d, skipDeleteNamespace=%v\n", chaosIterations, chaosSeed, skipDeleteNamespace)
	},
)

// SynchronizedAfterSuite ensures cleanup runs safely across all parallel processes.
var _ = SynchronizedAfterSuite(
	func() {
		By("cleaning up test context")
		if cancel != nil {
			cancel()
		}
	},
	func() {
		By("final cleanup complete")
	},
)

// parseInt parses an integer string and returns a default if parsing fails.
func parseInt(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}
