// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/test/e2e/internal/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	metricsAddr = ":8080"
	CrdFileName = "deployment"
)

var (
	testEnv            *envtest.Environment
	crdDirectoryPath   = filepath.Join("..", "..", CrdFileName)
	k8sClient          client.Client
	k8sManagerCancelFn context.CancelFunc
	ctx                context.Context
)

func TestE2e(t *testing.T) {
	RunSpecs(t, "E2e Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	RegisterFailHandler(Fail)

	SetDefaultEventuallyTimeout(time.Second * 10)

	useCluster := true
	By("configuring test environment")
	testEnv = &envtest.Environment{
		UseExistingCluster:       &useCluster,
		AttachControlPlaneOutput: true,
		CRDDirectoryPaths:        []string{crdDirectoryPath},
		ErrorIfCRDPathMissing:    true,
	}

	By("starting test environment")
	time.Sleep(10 * time.Second)
	cfg, err := testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	By("updating scheme")
	err = redisv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = metav1.AddMetaToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = apiextensions.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	By("creating manager")
	k8sManager, err := ctrl.NewManager(testEnv.Config, ctrl.Options{
		NewClient: config.NewClient(),
		Scheme:    scheme.Scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				RedisNamespace: {},
			},
		},
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
	})
	Expect(err).ToNot(HaveOccurred())

	k8sClient = k8sManager.GetClient()
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).ToNot(BeNil())

	go func() {
		ctx, k8sManagerCancelFn = context.WithCancel(ctrl.SetupSignalHandler())
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()
})

var _ = AfterSuite(func() {
	redisClusterOperator, err := createOperator()
	Expect(err).NotTo(HaveOccurred())

	By("tearing down the test environment")
	Expect(k8sClient.Delete(context.Background(), &redisClusterOperator)).Should(Succeed())

	if k8sManagerCancelFn != nil {
		k8sManagerCancelFn()
	}
	k8sManagerCancelFn = nil
	By("tearing down the test environment")
	Expect(testEnv).ToNot(BeNil())
	err = testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
