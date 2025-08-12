// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	finalizer "github.com/inditextech/redisoperator/internal/finalizers"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var k8sClient client.Client
var testEnv *envtest.Environment
var cluster *redisv1.RedKeyCluster = CreateRedisCluster()

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "ops", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = redisv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})

	Expect(err).ToNot(HaveOccurred())
	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).NotTo(BeNil())

	maxConcurrentReconciles := 10
	concurrentMigrates := 3

	reconciler := NewRedisClusterReconciler(k8sManager, maxConcurrentReconciles, concurrentMigrates)

	err = reconciler.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

	cluster := CreateRedisCluster()
	Expect(k8sClient.Create(ctx, cluster)).Should(Succeed())
})

// Add Ginkgo decorators
var _ = Describe("Your Test Suite Name", func() {
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

})

var _ = Describe("Reconciler", func() {
	BeforeEach(func() {
		EnsureClusterExistsOrCreate(types.NamespacedName{Name: cluster.Name, Namespace: "default"})
	})
	AfterEach(func() {

	})
	Context("CRD object", func() {
		When("CRD is submitted", func() {
			It("Can be found", func() {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, &redisv1.RedKeyCluster{})
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
	Context("StatefulSet", func() {
		When("Cluster declaration is submitted", func() {
			It("Sets correct owner", func() {
				sset := &v1.StatefulSet{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, sset)
				Expect(err).ToNot(HaveOccurred())
				controller := metav1.GetControllerOf(sset)
				Expect(controller.Kind).To(Equal("RedisCluster"))
			})
			It("Creates configmap", func() {
				cmap := &corev1.ConfigMap{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, cmap)
				Expect(err).ToNot(HaveOccurred())
				Expect(cmap.Data["redis.conf"]).To(ContainSubstring("maxmemory 500mb"))
				Expect(cmap.Data["redis.conf"]).To(ContainSubstring("cluster-enabled yes"))
			})
			It("Stateful set is created", func() {
				sset := &v1.StatefulSet{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, sset)
				log.Log.Error(err, "error", "statefulset", sset.Spec.Template.Spec.Containers[0].Resources)
				Expect(err).ToNot(HaveOccurred())
				cmap := &corev1.ConfigMap{}
				err = k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, cmap)
				Expect(err).ToNot(HaveOccurred())
				Expect(sset.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("800Mi"))

			})
			It("Stateful set is created with custom resource limits", func() {
				clusterName := "resources-labels-cluster-test"

				// create test cluster
				scluster := CreateRedisCluster()
				scluster.SetName(clusterName)
				scluster.Spec.Labels = &map[string]string{"belongsto": "team-a", "other": "label"}
				scluster.Spec.Resources = &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("599Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("599Mi"),
					},
				}
				Expect(k8sClient.Create(context.Background(), scluster)).Should(Succeed())
				time.Sleep(3 * time.Second)
				sset := &v1.StatefulSet{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: scluster.Name, Namespace: "default"}, sset)
				Expect(err).ToNot(HaveOccurred())
				Expect(sset.Spec.Template.Labels).To(Equal(scluster.Spec.Labels))
				Expect(sset.Labels).To(ContainElements("team-a", "label"))
				Expect(sset.Spec.Template.Spec.Containers[0].Resources).To(Equal(*scluster.Spec.Resources))

			})
			It("Service set is created", func() {
				svc := &corev1.Service{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, svc)
				Expect(err).ToNot(HaveOccurred())
			})

			It("Pod template labels are passed", func() {
				rcluster := &redisv1.RedKeyCluster{}
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, cluster)
				log.Log.Info("ncluster", "cluster", rcluster)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(rcluster.Spec.Robin.Template.GetObjectMeta().GetLabels())).To(Equal(2))
			})
			It("Configmap is deleted when passing finalizers", func() {
				err := k8sClient.Delete(context.Background(), cluster)
				Expect(err).ToNot(HaveOccurred())

				cm := &corev1.ConfigMap{}
				err = k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: "default"}, cm)
				Expect(err).To(HaveOccurred())

			})
		})
	})
	Context("Auth", func() {
		When("Secret passed", func() {
			It("Creates Configmap with the correct field", func() {
				secretName := "test-secret"
				clusterName := "secret-cluster-test"

				// create secret
				secret := &corev1.Secret{}
				secret.SetName(secretName)
				secret.SetNamespace("default")
				secret.StringData = map[string]string{"requirepass": "test123"}
				err := k8sClient.Create(context.Background(), secret)
				time.Sleep(1 * time.Second)
				Expect(err).ToNot(HaveOccurred())

				// create test cluster
				scluster := CreateRedisCluster()
				scluster.SetName(clusterName)
				scluster.Spec.Auth.SecretName = secretName
				Expect(k8sClient.Create(context.Background(), scluster)).Should(Succeed())
				time.Sleep(1 * time.Second)
				// get configmap and see the value has been set
				cmap := &corev1.ConfigMap{}
				err = k8sClient.Get(context.Background(), types.NamespacedName{Name: clusterName, Namespace: "default"}, cmap)
				Expect(err).ToNot(HaveOccurred())
				Expect(cmap.Data["redis.conf"]).To(ContainSubstring("requirepass test123"))

			})
		})
	})
})

func EnsureClusterExistsOrCreate(nsName types.NamespacedName) {
	rc := &redisv1.RedKeyCluster{}
	err := k8sClient.Get(context.TODO(), nsName, rc)
	if err != nil {
		k8sClient.Create(context.TODO(), CreateRedisCluster())
		time.Sleep(3 * time.Second)
	}
}

func CreateRedisCluster() *redisv1.RedKeyCluster {
	var finalizerId = (&finalizer.ConfigMapCleanupFinalizer{}).GetId()
	cluster := &redisv1.RedKeyCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RedisCluster",
			APIVersion: "redis.inditex.dev/redisv1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rediscluster-sample",
			Namespace:  "default",
			Finalizers: []string{finalizerId},
			Labels:     map[string]string{"team": "team-a"},
		},
		Spec: redisv1.RedKeyClusterSpec{
			Auth:     redisv1.RedisAuth{},
			Version:  "6.0.2",
			Replicas: 1,
			Config: `
			maxmemory 500mb

	`,
			Robin: &redisv1.RobinSpec{
				Template: &corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "monitor",
						Labels: map[string]string{"l1": "l1", "l2": "l2"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Image:   "test:1.4.36-alpine",
							Name:    "test",
							Command: []string{"test"},
							Ports: []corev1.ContainerPort{{
								ContainerPort: 9111,
								Name:          "test",
							}},
						}},
					},
				},
			},
		},
	}

	return cluster
}
