// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	defaultPoll = 10 * time.Second
	defaultWait = 100 * time.Second
)

var (
	k8sClient client.Client
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Redkey Operator E2E Suite", Label("e2e"))
}

// SynchronizedBeforeSuite ensures cluster-level setup runs once across all
// parallel Ginkgo processes. The first process (process 1) performs the
// one-time setup, and all processes then create their own Kubernetes client.
var _ = SynchronizedBeforeSuite(
	// This function runs ONLY on process 1 (the "primary" process).
	// It returns data that will be passed to all processes.
	func() []byte {
		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		By("registering schemes (process 1)")
		utilruntime.Must(redkeyv1.AddToScheme(scheme.Scheme))
		utilruntime.Must(corev1.AddToScheme(scheme.Scheme))
		utilruntime.Must(apiextensions.AddToScheme(scheme.Scheme))
		utilruntime.Must(metav1.AddMetaToScheme(scheme.Scheme))

		// Verify CRD directory exists for documentation purposes only.
		// The CRD should already be installed in the cluster.
		crdDir := filepath.Join("..", "..", "deployment")
		_, err := os.Stat(crdDir)
		Expect(err).NotTo(HaveOccurred(), "CRD directory %q must exist", crdDir)

		// Return empty data; all processes will create their own client.
		return nil
	},
	// This function runs on ALL processes (including process 1).
	// It receives the data returned by the first function.
	func(_ []byte) {
		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		By("creating Kubernetes client")

		// Register schemes on this process (they're per-process state)
		utilruntime.Must(redkeyv1.AddToScheme(scheme.Scheme))
		utilruntime.Must(corev1.AddToScheme(scheme.Scheme))
		utilruntime.Must(apiextensions.AddToScheme(scheme.Scheme))
		utilruntime.Must(metav1.AddMetaToScheme(scheme.Scheme))

		// Load kubeconfig from standard locations
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}

		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		Expect(err).NotTo(HaveOccurred(), "failed to load kubeconfig from %s", kubeconfig)
		Expect(cfg).NotTo(BeNil())

		// Create a simple controller-runtime client (no manager needed for E2E)
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred(), "failed to create Kubernetes client")
		Expect(k8sClient).NotTo(BeNil())

		// Create a cancellable context for all tests
		ctx, cancel = context.WithCancel(context.Background())
	},
)

// SynchronizedAfterSuite ensures cleanup runs safely across all parallel processes.
// The second function runs only on process 1 after all other processes have finished.
var _ = SynchronizedAfterSuite(
	// This function runs on ALL processes.
	func() {
		By("cleaning up test context")
		if cancel != nil {
			cancel()
		}
	},
	// This function runs ONLY on process 1, after all other processes have exited.
	func() {
		By("final cleanup complete")
		// No cluster-level teardown needed; we use an existing cluster.
	},
)
