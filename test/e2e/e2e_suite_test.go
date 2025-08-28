// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	crdDirName  = "deployment"
	metricsAddr = ":8080"
	defaultPoll = 10 * time.Second
	defaultWait = 100 * time.Second
)

var (
	testEnv       *envtest.Environment
	k8sClient     client.Client
	managerCancel context.CancelFunc
	ctx           context.Context
)

// NewCachingClientFunc returns a controller‑runtime NewClientFunc
// that enables Unstructured caching (i.e. watches on arbitrary CRDs).
// This is useful when you rely on types not registered in the built‑in scheme.
func NewCachingClientFunc() client.NewClientFunc {
	return func(cfg *rest.Config, opts client.Options) (client.Client, error) {
		// Turn on unstructured caching for CRDs / unknown types
		opts.Cache.Unstructured = true
		return client.New(cfg, opts)
	}
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RedKey Operator E2E Suite", Label("e2e"))
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	useCluster := true
	testEnv = &envtest.Environment{
		UseExistingCluster:       &useCluster,
		AttachControlPlaneOutput: true,
		CRDDirectoryPaths:        []string{filepath.Join("..", "..", crdDirName)},
		ErrorIfCRDPathMissing:    true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		if errors.IsAlreadyExists(err) {
			GinkgoWriter.Printf("warning: CRD already existed, continuing: %v\n", err)
			err = nil
		} else {
			Fail(fmt.Sprintf("failed to start test environment: %v", err))
		}
	}
	Expect(cfg).NotTo(BeNil())
	Expect(err).NotTo(HaveOccurred())

	// register all schemes in one shot
	utilruntime.Must(redkeyv1.AddToScheme(scheme.Scheme))
	utilruntime.Must(corev1.AddToScheme(scheme.Scheme))
	utilruntime.Must(apiextensions.AddToScheme(scheme.Scheme))
	utilruntime.Must(metav1.AddMetaToScheme(scheme.Scheme))

	port := 8080 + GinkgoParallelProcess() - 1
	metricsAddr := fmt.Sprintf(":%d", port)

	By("creating controller manager")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		NewClient: NewCachingClientFunc(),
		Scheme:    scheme.Scheme,
		Cache:     cache.Options{DefaultNamespaces: map[string]cache.Config{}},
		Metrics:   server.Options{BindAddress: metricsAddr},
	})

	Expect(err).NotTo(HaveOccurred())

	k8sClient = mgr.GetClient()
	Expect(k8sClient).NotTo(BeNil())

	ctx, managerCancel = context.WithCancel(ctrl.SetupSignalHandler())
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	By("shutting down test environment")
	if managerCancel != nil {
		managerCancel()
	}
	Expect(testEnv.Stop()).To(Succeed())
})
