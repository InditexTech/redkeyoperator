// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/test/e2e/framework"
)

const (
	RedisClusterName = "rediscluster-test"
	version          = "6.0.2"
)

// helper: creates a namespace with a GenerateName prefix and waits for it to be ready
func createNamespace(ctx context.Context, c client.Client, prefix string) *corev1.Namespace {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
		},
	}
	Expect(c.Create(ctx, ns)).To(Succeed())
	// wait until it's admitted
	Eventually(func() bool {
		var tmp corev1.Namespace
		return c.Get(ctx, client.ObjectKey{Name: ns.Name}, &tmp) == nil
	}, defaultWait, defaultPoll).Should(BeTrue())
	return ns
}

// deleteNamespace tears down everything in the namespace, including
// RedisCluster CRs with finalizers, then deletes the namespace itself.
func deleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) {
	// 1) Remove any RedisCluster CRs so their finalizers don't stall namespace deletion
	var rcList redisv1.RedisClusterList
	Expect(c.List(ctx, &rcList, &client.ListOptions{Namespace: ns.Name})).To(Succeed())

	for i := range rcList.Items {
		rc := &rcList.Items[i]
		// strip all finalizers
		rc.Finalizers = nil
		Expect(c.Update(ctx, rc)).To(Succeed(), "removing finalizers from %s/%s", rc.Namespace, rc.Name)
		// delete the CR immediately
		Expect(c.Delete(ctx, rc)).To(Succeed(), "deleting RedisCluster %s/%s", rc.Namespace, rc.Name)
	}

	// 2) Delete the namespace
	Expect(c.Delete(ctx, ns)).To(Succeed(), "deleting namespace %s", ns.Name)

	// 3) Wait for the namespace to actually disappear
	Eventually(func() bool {
		err := c.Get(ctx, types.NamespacedName{Name: ns.Name}, &corev1.Namespace{})
		return err != nil
	}, defaultWait, defaultPoll).Should(BeTrue(), "namespace %s should be gone", ns.Name)
}

var _ = Describe("Redis Operator & RedisCluster", Label("operator", "cluster"), func() {
	var (
		namespace *corev1.Namespace
	)

	BeforeEach(func() {
		// 1) create a fresh namespace per spec
		namespace = createNamespace(ctx, k8sClient, fmt.Sprintf("redis-e2e-%d", GinkgoParallelProcess()))

		// 2) install the operator into that namespace
		By("installing the Redis Operator into " + namespace.Name)
		Expect(EnsureOperatorSetup(ctx, namespace.Name)).To(Succeed())
	})

	AfterEach(func() {
		if namespace != nil {
			deleteNamespace(ctx, k8sClient, namespace)
		}
	})

	Context("when the operator is installed", func() {
		It("should deploy its controller Deployment and become healthy", func() {
			operatorDep := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx,
					client.ObjectKey{Namespace: namespace.Name, Name: "redis-operator"},
					operatorDep,
				)
				return err == nil && operatorDep.Status.AvailableReplicas >= 1
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})
	})

	Context("when managing a RedisCluster with 3 replicas", func() {
		It("creates a cluster", func() {
			key := types.NamespacedName{Namespace: namespace.Name, Name: "rdcl-test"}

			Expect(framework.EnsureClusterExistsOrCreate(
				ctx,
				k8sClient,
				key,
				3,
				0,
				"",
				true,
				true,
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())

			By("waiting for it to become Ready")
			Eventually(func() error {
				_, err := framework.WaitForReady(ctx, k8sClient, key)
				return err
			}, defaultWait*2, defaultPoll).Should(Succeed())

			// now fetch and assert
			rd := &redisv1.RedisCluster{}
			Expect(k8sClient.Get(ctx, key, rd)).To(Succeed())
			Expect(rd.Spec.Replicas).To(Equal(int32(3)))
			Expect(rd.Kind).To(Equal("RedisCluster"))
			Expect(rd.APIVersion).To(Equal("redis.inditex.com/v1"))
			Expect(rd.Spec.Auth).To(Equal(redisv1.RedisAuth{}))
			Expect(rd.Name).To(Equal("rdcl-test"))
			Expect(rd.Spec.Version).To(Equal(version))
		})
	})

	Context("when scaling a RedisCluster from zero to one replica and to zero again", func() {
		It("should scale up correctly and remain healthy", func() {
			key := types.NamespacedName{Namespace: namespace.Name, Name: "scale-test"}

			By("creating a RedisCluster with 0 replicas")
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				0,  // replicas
				0,  // replicasPerMaster
				"", // storage
				true,
				true,
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())

			By("waiting for initial Ready status")
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			By("scaling the cluster up to 3 replicas")
			rd, err := framework.ChangeConfiguration(ctx, k8sClient, key,
				3, 0, // replicas, replicasPerMaster
				true,          // ephemeral
				nil,           // resources
				"",            // image
				redisv1.Pdb{}, // pdb
				redisv1.RedisClusterOverrideSpec{},
				[]string{"ScalingUp", "Ready"}, // expected status
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Name).To(Equal("scale-test"))
			Expect(rd.Spec.Replicas).To(Equal(int32(3)))
			Expect(rd.Status.Status).To(Equal("Ready"))

			By("scaling the cluster up to 5 replicas")
			rd, err = framework.ChangeConfiguration(ctx, k8sClient, key,
				5, 0, // replicas, replicasPerMaster
				true,          // ephemeral
				nil,           // resources
				"",            // image
				redisv1.Pdb{}, // pdb
				redisv1.RedisClusterOverrideSpec{},
				[]string{"ScalingUp", "Ready"}, // expected status
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Name).To(Equal("scale-test"))
			Expect(rd.Spec.Replicas).To(Equal(int32(5)))
			Expect(rd.Status.Status).To(Equal("Ready"))

			By("scaling the cluster down to 3 replicas")
			rd, err = framework.ChangeConfiguration(ctx, k8sClient, key,
				3, 0, // replicas, replicasPerMaster
				true,          // ephemeral
				nil,           // resources
				"",            // image
				redisv1.Pdb{}, // pdb
				redisv1.RedisClusterOverrideSpec{},
				[]string{"ScalingDown", "Ready"}, // expected status
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Name).To(Equal("scale-test"))
			Expect(rd.Spec.Replicas).To(Equal(int32(3)))
			Expect(rd.Status.Status).To(Equal("Ready"))

			By("scaling the cluster down to 0 replicas")
			rd, err = framework.ChangeConfiguration(ctx, k8sClient, key,
				0, 0, // replicas, replicasPerMaster
				true,          // ephemeral
				nil,           // resources
				"",            // image
				redisv1.Pdb{}, // pdb
				redisv1.RedisClusterOverrideSpec{},
				[]string{"Ready"}, // expected status
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Name).To(Equal("scale-test"))

			By("waiting for initial Ready status")
			rd, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(int32(0)))
			Expect(rd.Status.Status).To(Equal("Ready"))
		})
	})

	Context("Create a RedisCluster with override defined and update it", func() {
		It("should apply podTemplate labels, tolerations and topologySpread from override", func() {
			key := types.NamespacedName{Namespace: namespace.Name, Name: "override-test"}

			override := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"testLabel": "testLabel"},
							},
							Spec: corev1.PodSpec{
								Tolerations: []corev1.Toleration{{
									Key:      "testToleration",
									Operator: corev1.TolerationOpExists,
									Effect:   corev1.TaintEffectNoSchedule,
								}},
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
									MaxSkew:           1,
									TopologyKey:       "kubernetes.io/hostname",
									WhenUnsatisfiable: corev1.DoNotSchedule,
									LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel"}},
								}},
							},
						},
					},
				},
			}

			By("creating a RedisCluster with 1 replicas and overide")
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				1,  // replicas
				0,  // replicasPerMaster
				"", // storage
				true,
				true,
				redisv1.Pdb{},
				override,
			)).To(Succeed())

			By("waiting for initial Ready status")
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			// inspect the live StatefulSet
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(framework.RedisStsContainsOverride(*sts, override)).To(BeTrue())

			By("update override to add sidecar & new labels/topology")
			// new override: add sidecar & extra label/topology
			updated := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"testLabel": "testLabel", "testLabel2": "testLabel2"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:    "test-sidecar",
										Image:   "axinregistry1.central.inditex.grp/ubi9/ubi:9.0.0",
										Command: []string{"sleep", "infinity"},
										SecurityContext: &corev1.SecurityContext{
											RunAsNonRoot: ptr.To(true),
											RunAsUser:    ptr.To(int64(1000)),
										},
									},
								},
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
									MaxSkew:           1,
									TopologyKey:       "kubernetes.io/hostname",
									WhenUnsatisfiable: corev1.DoNotSchedule,
									LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel2"}},
								}},
								SecurityContext: &corev1.PodSecurityContext{
									RunAsNonRoot: ptr.To(true),
									RunAsUser:    ptr.To(int64(1000)),
								},
							},
						},
					},
				},
			}

			rd, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Override = &updated
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Override).To(Equal(&updated))

			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(framework.RedisStsContainsOverride(*sts, updated)).To(BeTrue())

			By("Remove the sidecar from the override")
			// now remove the sidecar (empty PodTemplateSpec.Spec.Containers)
			noSidecar := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: nil,
								SecurityContext: &corev1.PodSecurityContext{
									RunAsNonRoot: ptr.To(true),
									RunAsUser:    ptr.To(int64(1000)),
								},
							},
						},
					},
				},
			}
			rd, err = framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Override = &noSidecar
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Override.StatefulSet.Spec.Template.Spec.Containers).To(BeEmpty())
		})
	})
	Context("when managing a RedisCluster Service ports", func() {
		It("should manage service ports correctly", func() {
			key := types.NamespacedName{Namespace: namespace.Name, Name: "service-ports-test"}

			// 1) Create & wait for Ready
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				3, 0, "", true, true,
				redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			// Helper to read the service's ports
			getPorts := func() []int32 {
				svc := &corev1.Service{}
				if err := k8sClient.Get(ctx, key, svc); err != nil {
					return nil
				}
				ports := make([]int32, len(svc.Spec.Ports))
				for i, p := range svc.Spec.Ports {
					ports[i] = p.Port
				}
				sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
				return ports
			}

			// 2) Initially only the comm port should exist
			Expect(getPorts()).To(Equal([]int32{redis.RedisCommPort}))

			// 3) Remove all ports; Eventually we should see it restored to [commPort]
			Expect(framework.RemoveServicePorts(ctx, k8sClient, key)).To(Succeed())
			Eventually(getPorts, defaultWait*2, defaultPoll).
				Should(Equal([]int32{redis.RedisCommPort}), "operator should restore the comm port")

			// 4) Add extra ports; Eventually it should again correct back to [commPort]
			Expect(framework.AddServicePorts(ctx, k8sClient, key)).To(Succeed())
			Eventually(getPorts, defaultWait*2, defaultPoll).
				Should(Equal([]int32{redis.RedisCommPort}), "operator should prune invalid ports back to comm")
		})
	})

	Context("when managing spec.labels", func() {
		It("should add spec.labels to StatefulSet and Pods", func() {
			key := types.NamespacedName{Namespace: namespace.Name, Name: "service-ports-test"}

			// 0) Create & wait for Ready
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				3, 0, "", true, true,
				redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			// 1) Mutate Spec.Labels
			specLabels := map[string]string{"team": "teamA", "testSpecLabel": "testSpec"}
			rd, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Labels = &specLabels
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Labels).To(Equal(&specLabels))

			// expected on the STS/CM/Pods:
			stsLabels := map[string]string{
				"redis-cluster-name":                    key.Name,
				"redis.rediscluster.operator/component": "redis",
				"team":                                  "teamA",
				"testSpecLabel":                         "testSpec",
			}

			// 2) StatefulSet labels
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(sts.Spec.Template.Labels).To(Equal(stsLabels))

			// 3) ConfigMap labels
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, key, cm)).To(Succeed())
			Expect(cm.Labels).To(Equal(stsLabels))

			// 4) Pod labels (first pod index 0)
			podKey := types.NamespacedName{Name: key.Name + "-0", Namespace: key.Namespace}
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
			for k, v := range stsLabels {
				Expect(pod.Labels[k]).To(Equal(v))
			}

			By("remove spec.labels from StatefulSet and Pods")
			// 2) Now clear spec.labels
			rd, err = framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Labels = &map[string]string{} // empty map
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(*rd.Spec.Labels).To(BeEmpty())

			// base STS/CM labels (only operator ones)
			baseLabels := map[string]string{
				"redis-cluster-name":                    key.Name,
				"redis.rediscluster.operator/component": "redis",
			}

			// 3) StatefulSet labels reset
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(sts.Spec.Template.Labels).To(Equal(baseLabels))

			// 4) ConfigMap labels reset
			Expect(k8sClient.Get(ctx, key, cm)).To(Succeed())
			Expect(cm.Labels).To(Equal(baseLabels))

			// 5) Pod labels reset
			podKey = types.NamespacedName{Name: key.Name + "-0", Namespace: key.Namespace}
			Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
			for k, v := range baseLabels {
				Expect(pod.Labels[k]).To(Equal(v))
			}
		})
	})

	Context("when RedisCluster has storage", func() {
		const (
			initialStorage = "500Mi"
			desiredStorage = "1Gi"
		)
		var (
			key             types.NamespacedName
			initialReplicas int32 = 3
		)

		BeforeEach(func() {
			key = types.NamespacedName{Name: "storage-management", Namespace: namespace.Name}
			// create cluster with PVC-backed storage
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				initialReplicas, 0,
				initialStorage,
				true,  // purgeKeys
				false, // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			// block until Ready
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should forbid changing storage size and leave it unchanged", func() {
			_, err := framework.ChangeStorage(ctx, k8sClient, key, desiredStorage, initialReplicas)
			Expect(err).To(MatchError(ContainSubstring("Changing the storage size is not allowed")))

			// cluster stays Ready with original storage
			rd, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Storage).To(Equal(initialStorage))

			// final health check
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("should scale up with storage in place", func() {
			expected := int32(6)
			rd, err := framework.ChangeConfiguration(
				ctx, k8sClient, key,
				expected, 0, // replicas, replicasPerMaster
				false,         // ephemeral
				nil,           // resources
				"",            // image
				redisv1.Pdb{}, // pdb
				redisv1.RedisClusterOverrideSpec{},
				[]string{"ScalingUp", "Ready"},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(expected))

			// block until Ready again
			rd, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(expected))

			// final health check
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("should scale down with storage in place", func() {
			expected := int32(1)
			rd, err := framework.ChangeConfiguration(
				ctx, k8sClient, key,
				expected, 0,
				false,
				nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{},
				[]string{"ScalingDown", "Ready"},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(expected))

			// block until Ready again
			rd, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(expected))

			// final health check
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})
	})
	Context("when RedisCluster has master-replica configuration", func() {
		const (
			baseName         = "master-management"
			initialReplicas  = int32(3)
			initialPerMaster = int32(1)
		)
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{
				Name:      baseName,
				Namespace: namespace.Name,
			}
			// 1) create base master-replica cluster
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				initialReplicas, initialPerMaster,
				"",   // no storage
				true, // purgeKeys
				true, // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			// 2) wait for Ready
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates a new RedisCluster in master-slave mode correctly", func() {
			// validate master-slave layout
			Eventually(func() (bool, error) {
				return framework.ValidateRedisClusterMasterSlave(
					ctx, k8sClient, key,
					initialReplicas, initialPerMaster,
				)
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// final health check
			rd, _ := framework.WaitForReady(ctx, k8sClient, key)
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("scales up a master-slave RedisCluster correctly", func() {
			const (
				upReplicas  = int32(4)
				upPerMaster = int32(2)
			)
			rd, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingUp", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = upReplicas
					rc.Spec.ReplicasPerMaster = upPerMaster
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(upReplicas))
			Expect(rd.Spec.ReplicasPerMaster).To(Equal(upPerMaster))

			// validate new layout and health
			Eventually(func() (bool, error) {
				return framework.ValidateRedisClusterMasterSlave(
					ctx, k8sClient, key,
					upReplicas, upPerMaster,
				)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("scales down a master-slave RedisCluster correctly", func() {
			const (
				downReplicas  = int32(3)
				downPerMaster = int32(1)
			)
			rd, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingDown", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = downReplicas
					rc.Spec.ReplicasPerMaster = downPerMaster
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Replicas).To(Equal(downReplicas))
			Expect(rd.Spec.ReplicasPerMaster).To(Equal(downPerMaster))

			// validate new layout and health
			Eventually(func() (bool, error) {
				return framework.ValidateRedisClusterMasterSlave(
					ctx, k8sClient, key,
					downReplicas, downPerMaster,
				)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())

		})
	})

	Context("when RedisCluster has master-replica configuration", func() {
		const (
			baseName        = "data-management"
			initialReplicas = int32(5)
			upReplicas      = int32(7)
			downReplicas    = int32(3)
		)
		var (
			key types.NamespacedName
			rd  *redisv1.RedisCluster
		)

		BeforeEach(func() {
			key = types.NamespacedName{
				Name:      baseName,
				Namespace: namespace.Name,
			}
			// 1) create & wait for Ready
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				initialReplicas, 0,
				"",    // storage
				false, // purgeKeys
				true,  // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())

			var err error
			rd, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should insert data inside the cluster correctly", func() {
			// insert some keys
			Eventually(func() error {
				ok, err := framework.InsertDataIntoCluster(ctx, k8sClient, key, rd)
				if !ok {
					return fmt.Errorf("insert failed: %w", err)
				}
				return err
			}, defaultWait*2, defaultPoll).Should(Succeed())

			// verify data distribution
			Eventually(func() (bool, error) {
				return framework.CheckRedisCluster(k8sClient, ctx, rd)
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// verify data integrity
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd)
				return framework.CheckClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
			// flush keys
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd)
				return framework.FlushClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("should insert data & scale up correctly", func() {
			// insert first
			Eventually(func() error {
				ok, err := framework.InsertDataIntoCluster(ctx, k8sClient, key, rd)
				if !ok {
					return fmt.Errorf("insert failed: %w", err)
				}
				return err
			}, defaultWait*2, defaultPoll).Should(Succeed())

			// scale up
			rd2, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingUp", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = upReplicas
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd2.Spec.Replicas).To(Equal(upReplicas))

			// verify data still there
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd2)
				return framework.CheckClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// flush after scale
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd2)
				return framework.FlushClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})

		It("should insert data & scale down correctly", func() {
			// insert first
			Eventually(func() error {
				ok, err := framework.InsertDataIntoCluster(ctx, k8sClient, key, rd)
				if !ok {
					return fmt.Errorf("insert failed: %w", err)
				}
				return err
			}, defaultWait*2, defaultPoll).Should(Succeed())

			// scale down
			rd3, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingDown", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = downReplicas
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rd3.Spec.Replicas).To(Equal(downReplicas))

			// verify data integrity on *current* pods
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd3)
				return framework.CheckClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// flush keys on current pods
			Eventually(func() (bool, error) {
				pods := framework.GetPods(k8sClient, ctx, rd3)
				return framework.FlushClusterKeys(pods)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})
	})

	Context("when RedisCluster requires StatefulSet updates", func() {
		const baseName = "resources-management"
		var (
			key types.NamespacedName
		)

		BeforeEach(func() {
			key = types.NamespacedName{
				Name:      baseName,
				Namespace: namespace.Name,
			}
			// Create a 1-node cluster and wait for Ready
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				1, 0, // replicas, perMaster
				"",   // no storage
				true, // purgeKeys
				true, // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should propagate resource changes to the StatefulSet", func() {
			expected := corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("51m"),
					corev1.ResourceMemory: resource.MustParse("51Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("51m"),
					corev1.ResourceMemory: resource.MustParse("51Mi"),
				},
			}

			// Mutate the CR’s Spec.Resources
			_, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Resources = &expected
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Wait for it to become Ready again
			_, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			// Fetch the live StatefulSet and verify its container’s resources
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			actual := sts.Spec.Template.Spec.Containers[0].Resources
			Expect(actual).To(Equal(expected))
		})

		It("should propagate image changes to the StatefulSet", func() {
			expectedImage := "axinregistry1.central.inditex.grp/itxapps/redis.redis-stack-server:7.2.0-v10-coordinator"

			// Mutate the CR’s Spec.Image
			_, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Upgrading", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Image = expectedImage
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Wait for it to become Ready again
			_, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())

			// Fetch the live StatefulSet and verify its container’s image
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal(expectedImage))
		})
	})
	Context("when RedisCluster is ephemeral", func() {
		const (
			baseName        = "resources-management"
			initialStorage  = "1Gi"
			initialReplicas = int32(1)
		)
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", baseName, RedisClusterName),
				Namespace: namespace.Name,
			}
			// create an ephemeral cluster
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				initialReplicas, 0,
				"",   // no storage
				true, // purgeKeys
				true, // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			// wait until it's Ready
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should forbid switching from ephemeral to storage", func() {
			// Attempt to flip ephemeral -> storage
			_, err := framework.ChangeCluster(ctx, k8sClient, key, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				// we expect the mutation to fail before any status change
				NextStatuses: nil,
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Ephemeral = false
					rc.Spec.Storage = initialStorage
				},
			})
			Expect(err).To(MatchError(ContainSubstring("Changing the ephemeral field is not allowed")))

			// Ensure the cluster remained Ready and ephemeral=true
			rd, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(rd.Spec.Ephemeral).To(BeTrue())
			Expect(rd.Spec.Storage).To(BeEmpty())
		})
	})

	Context("when RedisCluster PodDisruptionBudget is toggled", func() {
		const (
			baseName        = "poddisruptionbudget"
			initialReplicas = int32(3)
			pdbMinAvailable = int32(1)
		)
		var (
			rcKey  types.NamespacedName
			pdbKey types.NamespacedName
		)

		BeforeEach(func() {
			rcKey = types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", baseName, RedisClusterName),
				Namespace: namespace.Name,
			}
			pdbKey = types.NamespacedName{
				Name:      rcKey.Name + "-pdb",
				Namespace: rcKey.Namespace,
			}

			// create a 3-replica cluster with no PDB
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, rcKey,
				initialReplicas, 0,
				"",            // no storage
				true,          // purgeKeys
				true,          // ephemeral
				redisv1.Pdb{}, // disabled
				redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			// wait until RedisCluster is Ready
			_, err := framework.WaitForReady(ctx, k8sClient, rcKey)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates, deletes and re-creates the PDB as spec.Pdb.Enabled toggles", func() {
			pdb := &policyv1.PodDisruptionBudget{}

			// 1) Enable PDB ⇒ it should appear
			_, err := framework.ChangeCluster(ctx, k8sClient, rcKey, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Pdb = redisv1.Pdb{
						Enabled:          true,
						PdbSizeAvailable: intstr.FromInt(int(pdbMinAvailable)),
					}
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				return k8sClient.Get(ctx, pdbKey, pdb)
			}, defaultWait*2, defaultPoll).Should(Succeed())
			Expect(pdb.Spec.MinAvailable.IntVal).To(Equal(pdbMinAvailable))

			// 2) Disable PDB ⇒ it should be deleted
			_, err = framework.ChangeCluster(ctx, k8sClient, rcKey, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Pdb.Enabled = false
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				return k8sClient.Get(ctx, pdbKey, &policyv1.PodDisruptionBudget{}) != nil
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// 3) Re-enable PDB ⇒ it should reappear
			_, err = framework.ChangeCluster(ctx, k8sClient, rcKey, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Pdb.Enabled = true
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				return k8sClient.Get(ctx, pdbKey, pdb)
			}, defaultWait*2, defaultPoll).Should(Succeed())
			Expect(pdb.Spec.MinAvailable.IntVal).To(Equal(pdbMinAvailable))

			// 4) Scale down to 0 ⇒ even though Enabled=true, PDB must be removed
			_, err = framework.ChangeCluster(ctx, k8sClient, rcKey, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingDown", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = 0
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				return k8sClient.Get(ctx, pdbKey, &policyv1.PodDisruptionBudget{}) != nil
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// 5) Scale back up ⇒ PDB returns
			_, err = framework.ChangeCluster(ctx, k8sClient, rcKey, framework.ChangeClusterOptions{
				CurrentStatus: "Ready",
				NextStatuses:  []string{"ScalingUp", "Ready"},
				Mutate: func(rc *redisv1.RedisCluster) {
					rc.Spec.Replicas = initialReplicas
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				return k8sClient.Get(ctx, pdbKey, pdb)
			}, defaultWait*2, defaultPoll).Should(Succeed())
			Expect(pdb.Spec.MinAvailable.IntVal).To(Equal(pdbMinAvailable))
		})
	})

	Context("when RedisCluster nodes are being reconfigured", func() {
		const (
			baseName = "cluster-meet"
			replicas = int32(3)
		)
		var (
			key types.NamespacedName
		)

		BeforeEach(func() {
			key = types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", baseName, RedisClusterName),
				Namespace: namespace.Name,
			}
			// 1) Create & wait for Ready
			Expect(framework.EnsureClusterExistsOrCreate(
				ctx, k8sClient, key,
				replicas, 0,
				"",
				true, // purgeKeys
				true, // ephemeral
				redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{},
			)).To(Succeed())
			_, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should allow forgetting one node and self-heal", func() {
			// 2) Ensure cluster is healthy
			Eventually(func() (bool, error) {
				rc, err := framework.WaitForReady(ctx, k8sClient, key)
				if err != nil {
					return false, err
				}
				return framework.CheckRedisCluster(k8sClient, ctx, rc)
			}, defaultWait*3, defaultPoll).Should(BeTrue())

			// 3) Forget the first node
			rc, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.ForgetANode(k8sClient, ctx, rc)).To(Succeed())

			// 4) Wait for the operator to reconcile and cluster to be Ready again
			Eventually(func() error {
				_, err := framework.WaitForReady(ctx, k8sClient, key)
				return err
			}, defaultWait*4, defaultPoll).Should(Succeed())

			// 5) Final health check
			rc2, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.CheckRedisCluster(k8sClient, ctx, rc2)).To(BeTrue())
		})

		It("should use fix/meet to rebalance after operator outage by scaling operator to zero", func() {
			// 2) Ensure cluster is Ready and healthy
			rc, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.CheckRedisCluster(k8sClient, ctx, rc)).To(BeTrue())

			// 3) Simulate operator outage by scaling its Deployment to 0
			depKey := client.ObjectKey{Namespace: namespace.Name, Name: "redis-operator"}
			opDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, depKey, opDep)).To(Succeed())

			// Scale down
			zero := int32(0)
			opDep.Spec.Replicas = &zero
			Expect(k8sClient.Update(ctx, opDep)).To(Succeed())
			// Wait until no operator pods are running
			Eventually(func() bool {
				var d appsv1.Deployment
				if err := k8sClient.Get(ctx, depKey, &d); err != nil {
					return false
				}
				return d.Status.AvailableReplicas == 0
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// 4) Forget+fix+meet to rejoin nodes
			Expect(framework.ForgetANodeFixAndMeet(k8sClient, ctx, rc)).To(Succeed())

			// 5) Scale the operator back up to 1 replica
			Expect(k8sClient.Get(ctx, depKey, opDep)).To(Succeed())
			one := int32(1)
			opDep.Spec.Replicas = &one
			Expect(k8sClient.Update(ctx, opDep)).To(Succeed())
			// Wait until operator is available again
			Eventually(func() bool {
				var d appsv1.Deployment
				if err := k8sClient.Get(ctx, depKey, &d); err != nil {
					return false
				}
				return d.Status.AvailableReplicas >= 1
			}, defaultWait*2, defaultPoll).Should(BeTrue())

			// 6) Finally wait for the cluster to return to Ready+healthy
			_, err = framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			rc2, err := framework.WaitForReady(ctx, k8sClient, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(framework.CheckRedisCluster(k8sClient, ctx, rc2)).To(BeTrue())
		})
	})
})
