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
	k8sClientset      kubernetes.Interface
	dynamicClient     dynamic.Interface
	ctx               context.Context
	cancel            context.CancelFunc
	chaosDuration     time.Duration
	chaosSeed         int64
	chaosReadyTimeout = 10 * time.Minute
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

		chaosDuration = parseDuration(os.Getenv("CHAOS_DURATION"), 10*time.Minute)

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

		GinkgoWriter.Printf("Chaos test configuration: duration=%v, seed=%d\n", chaosDuration, chaosSeed)
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

// parseDuration parses a duration string and returns a default if parsing fails.
func parseDuration(s string, defaultVal time.Duration) time.Duration {
	if s == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return d
}
