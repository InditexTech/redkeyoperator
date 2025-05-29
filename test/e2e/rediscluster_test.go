// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	pv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/controllers"
	"github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/test/e2e/framework"
)

const (
	RedisClusterName = "rediscluster-test"
	version          = "6.0.2"
	redisOperator    = "redis-operator"
	Timeout          = 10 * time.Second
	PollingInterval  = 1 * time.Second
)

var _ = Describe("Redisclusters", func() {
	fetchedRedisClusterOperator := &appsv1.Deployment{}
	fetchedRedisCluster := &redisv1.RedisCluster{}
	namespacedName := types.NamespacedName{Name: RedisNamespace, Namespace: RedisNamespace}

	var _ = AfterEach(func() {
		if !CurrentSpecReport().Failure.IsZero() {
			// Test failed, clean up resources here
			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		}

	})

	Context("redis operator", func() {
		It("Should create redis operator correctly", func() {
			err := ensureNamespaceExistsOrCreate()
			Expect(err).ToNot(HaveOccurred())

			err = ensureServiceAccountExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator-sa",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			err = ensureRedisOperatorRoleExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator-role",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			err = ensureLeaderElectionRoleExistsOrCreate(types.NamespacedName{
				Name:      "leader-election-role",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			err = ensureLeaderElectionRoleBindingExistsOrCreate(types.NamespacedName{
				Name:      "leader-election-rolebinding",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			err = ensureRedisOperatorRoleBindingExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator-rolebinding",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			err = ensureConfigMapExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator-config",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			redisClusterOperator, err := createOperator()
			Expect(err).NotTo(HaveOccurred())

			err = ensureOperatorExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: redisClusterOperator.Name, Namespace: RedisNamespace}, fetchedRedisClusterOperator)
				return err == nil
			}).Should(BeTrue())
		})
	})
	Context("redis cluster", func() {
		It("Should create redisCluster with 0 replicas correctly", func() {
			initialReplicas := int32(0)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-no-replicas", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Kind).To(Equal("RedisCluster"))
			Expect(fetchedRedisCluster.APIVersion).To(Equal("redis.inditex.dev/v1"))
			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(initialReplicas))
			Expect(fetchedRedisCluster.Spec.Auth).To(Equal(redisv1.RedisAuth{}))
			Expect(fetchedRedisCluster.Name).To(Equal(rdclName))
			Expect(fetchedRedisCluster.Spec.Version).To(Equal(version))
		})

		It("Should scale up redisCluster correctly from 0 replicas", func() {
			initialReplicas := int32(0)
			expectedReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-no-replicas", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			time.Sleep(30 * time.Second)

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())
		})

		It("Should scale down redisCluster to 0 replicas correctly", func() {
			initialReplicas := int32(1)
			expectedReplicas := int32(0)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-no-replicas", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Ready", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			time.Sleep(30 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should add labels, tolerations and topology from spec.override", func() {
			initialReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "override", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			override := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"testLabel": "testLabel",
								},
							},
							Spec: corev1.PodSpec{
								Tolerations: []corev1.Toleration{
									{
										Key:      "testToleration",
										Operator: "Exists",
										Effect:   "NoSchedule",
									},
								},
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.DoNotSchedule,
										LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel"}},
									},
								},
							},
						},
					},
				},
			}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, override)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Override.StatefulSet).To(Equal(override.StatefulSet))

			Eventually(func() bool {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, namespacedName, sts)
				return err == nil && framework.RedisStsContainsOverride(*sts, override)
			}).Should(BeTrue())
		})

		It("Should remove tolerations, add sidecar and update labels and topology in spec.override", func() {
			initialReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "override", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			originalOverride := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"testLabel": "testLabel",
								},
							},
							Spec: corev1.PodSpec{
								Tolerations: []corev1.Toleration{
									{
										Key:      "testToleration",
										Operator: "Exists",
										Effect:   "NoSchedule",
									},
								},
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.DoNotSchedule,
										LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel"}},
									},
								},
							},
						},
					},
				},
			}

			override := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"testLabel":  "testLabel",
									"testLabel2": "testLabel2",
								},
							},
							Spec: corev1.PodSpec{
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.DoNotSchedule,
										LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel2"}},
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test-sidecar",
										Image: "nginx",
									},
								},
							},
						},
					},
				},
			}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, originalOverride)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Upgrading", initialReplicas, 0, true, nil, "", redisv1.Pdb{}, override)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Override.StatefulSet).To(Equal(override.StatefulSet))

			Eventually(func() bool {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, namespacedName, sts)
				return err == nil && framework.RedisStsContainsOverride(*sts, override)
			}).Should(BeTrue())
		})

		It("Should remove sidecar", func() {
			initialReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "override", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			originalOverride := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"testLabel": "testLabel",
								},
							},
							Spec: corev1.PodSpec{
								Tolerations: []corev1.Toleration{
									{
										Key:      "testToleration",
										Operator: "Exists",
										Effect:   "NoSchedule",
									},
								},
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.DoNotSchedule,
										LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel"}},
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test-sidecar",
										Image: "nginx",
									},
								},
							},
						},
					},
				},
			}

			override := redisv1.RedisClusterOverrideSpec{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"testLabel":  "testLabel",
									"testLabel2": "testLabel2",
								},
							},
							Spec: corev1.PodSpec{
								TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.DoNotSchedule,
										LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"testLabel": "testLabel2"}},
									},
								},
							},
						},
					},
				},
			}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, originalOverride)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Upgrading", initialReplicas, 0, true, nil, "", redisv1.Pdb{}, override)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Override.StatefulSet).To(Equal(override.StatefulSet))

			Eventually(func() bool {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, namespacedName, sts)
				return err == nil && framework.RedisStsContainsOverride(*sts, override)
			}).Should(BeTrue())

			time.Sleep(30 * time.Second)

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create redisCluster correctly", func() {
			initialReplicas := int32(3)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-installation", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Kind).To(Equal("RedisCluster"))
			Expect(fetchedRedisCluster.APIVersion).To(Equal("redis.inditex.dev/v1"))
			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(initialReplicas))
			Expect(fetchedRedisCluster.Spec.Auth).To(Equal(redisv1.RedisAuth{}))
			Expect(fetchedRedisCluster.Name).To(Equal(rdclName))
			Expect(fetchedRedisCluster.Spec.Version).To(Equal(version))

		})

		It("Should scale up redisCluster correctly", func() {
			initialReplicas := int32(3)
			expectedReplicas := int32(5)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-installation", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should scale down redisCluster correctly", func() {
			initialReplicas := int32(5)
			expectedReplicas := int32(3)
			rdclName := fmt.Sprintf("%s-%s", "properly-redis-installation", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingDown", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should manage service ports correctly", func() {
			objectsTestName := "service-ports"
			rdclName := fmt.Sprintf("%s-%s", objectsTestName, RedisClusterName)
			initialReplicas := int32(1)

			namespacedName := types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			fetchedService := &corev1.Service{}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			// Only comm port is set at Redis creation.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedService)
				return err == nil
			}).Should(BeTrue())

			Expect(len(fetchedService.Spec.Ports)).To(Equal(1))
			Expect(fetchedService.Spec.Ports[0].Port).To(Equal(int32(redis.RedisCommPort)))

			// Comm port is restablished if all ports removed from service.
			Eventually(func() bool {
				err := framework.RemoveServicePorts(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())
			time.Sleep((controllers.DEFAULT_REQUEUE_TIMEOUT + 20) * time.Second)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedService)
				return err == nil
			}).Should(BeTrue())

			Expect(len(fetchedService.Spec.Ports)).To(Equal(1))
			Expect(fetchedService.Spec.Ports[0].Port).To(Equal(int32(redis.RedisCommPort)))

			// Only comm port is set if service ports are altered.
			Eventually(func() bool {
				err := framework.AddServicePorts(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())
			time.Sleep((controllers.DEFAULT_REQUEUE_TIMEOUT + 20) * time.Second)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedService)
				return err == nil
			}).Should(BeTrue())

			Expect(len(fetchedService.Spec.Ports)).To(Equal(1))
			Expect(fetchedService.Spec.Ports[0].Port).To(Equal(int32(redis.RedisCommPort)))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should add the labels added to RedisCluster spec.labels to the StatefulSet and the Pods", func() {
			objectsTestName := "labels-management"
			rdclName := fmt.Sprintf("%s-%s", objectsTestName, RedisClusterName)
			expectedLabels := map[string]string{"team": "teamA", "testSpecLabel": "testSpec"}
			expectedLabelsSts := map[string]string{"redis-cluster-name": rdclName, "redis.rediscluster.operator/component": "redis", "team": "teamA", "testSpecLabel": "testSpec"}
			initialReplicas := int32(1)

			namespacedName := types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			fetchedStatefulSet := &appsv1.StatefulSet{}
			fetchedConfigMap := &corev1.ConfigMap{}
			fetchedPod := &corev1.Pod{}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, expectedLabels, "Upgrading", 1, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Labels).To(Equal(&expectedLabels))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedStatefulSet)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedStatefulSet.Spec.Template.Labels).To(Equal(expectedLabelsSts))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedConfigMap)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedConfigMap.Labels).To(Equal(expectedLabelsSts))

			Eventually(func() bool {
				rdclName = fmt.Sprintf("%s-%s-%d", objectsTestName, RedisClusterName, 0)
				namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
				err := k8sClient.Get(ctx, namespacedName, fetchedPod)
				return err == nil
			}).Should(BeTrue())

			for k, v := range expectedLabelsSts {
				Expect(fetchedPod.Labels[k]).To(Equal(v))
			}

		})

		It("should delete the labels added to RedisCluster spec.labels to the StatefulSet and the Pods", func() {
			objectsTestName := "labels-management"
			rdclName := fmt.Sprintf("%s-%s", objectsTestName, RedisClusterName)
			expectedLabelsSts := map[string]string{"redis-cluster-name": rdclName, "redis.rediscluster.operator/component": "redis"}
			expectedLabels := map[string]string{}
			initialReplicas := int32(1)
			namespacedName := types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			fetchedStatefulSet := &appsv1.StatefulSet{}
			fetchedConfigMap := &corev1.ConfigMap{}
			fetchedPod := &corev1.Pod{}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, expectedLabels, "Upgrading", 1, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Labels).To(Equal(&expectedLabels))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedStatefulSet)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedStatefulSet.Spec.Template.Labels).To(Equal(expectedLabelsSts))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedConfigMap)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedConfigMap.Labels).To(Equal(expectedLabelsSts))

			Eventually(func() bool {
				rdclName = fmt.Sprintf("%s-%s-%d", objectsTestName, RedisClusterName, 0)
				namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
				err := k8sClient.Get(ctx, namespacedName, fetchedPod)
				return err == nil
			}).Should(BeTrue())

			for k, v := range expectedLabelsSts {
				Expect(fetchedPod.Labels[k]).To(Equal(v))
			}

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should handle the storage in redisCluster correctly", func() {
			initialStorage := string("500Mi")
			desiredStorage := string("1Gi")
			rdclName := fmt.Sprintf("%s-%s", "storage-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			initialReplicas := int32(3)

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, initialStorage, 0, true, false, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			// Try to change the storage in redis-cluster
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterStorage(ctx, k8sClient, namespacedName, initialStorage, desiredStorage, initialReplicas)
				return err != nil && strings.Contains(err.Error(), "Changing the storage size is not allowed")
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Storage).To(Equal((initialStorage)))

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should scaleup with storage in redisCluster correctly", func() {
			initialStorage := string("500Mi")
			initialReplicas := int32(3)
			expectedReplicas := int32(6)
			rdclName := fmt.Sprintf("%s-%s", "storage-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, initialStorage, 0, true, false, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			// Try to scaleUp in redis-cluster
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", expectedReplicas, 0, false, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			time.Sleep(30 * time.Second)

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should scaledown with storage in redisCluster correctly", func() {
			initialStorage := string("500Mi")
			initialReplicas := int32(3)
			expectedReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "storage-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, initialStorage, 0, true, false, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingDown", expectedReplicas, 0, false, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedRedisCluster.Spec.Replicas).To(Equal(int32(expectedReplicas)))

			time.Sleep(60 * time.Second)

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should creates a new redisCluster in master-slave configuration correctly", func() {
			replicas := int32(3)
			replicasPerMaster := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "master-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", replicasPerMaster, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.ValidateRedisClusterMasterSlave(ctx, k8sClient, namespacedName, replicas, replicasPerMaster, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should scale up a redisCluster in master-slave configuration correctly", func() {
			replicas := int32(3)
			replicasPerMaster := int32(1)
			expectedReplicas := int32(4)
			expectedReplicasPerMaster := int32(2)
			rdclName := fmt.Sprintf("%s-%s", "master-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", replicasPerMaster, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", expectedReplicas, expectedReplicasPerMaster, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.ValidateRedisClusterMasterSlave(ctx, k8sClient, namespacedName, expectedReplicas, expectedReplicasPerMaster, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

		})
		It("Should scale down a redisCluster in master-slave configuration correctly", func() {
			replicas := int32(4)
			replicasPerMaster := int32(2)
			expectedReplicas := int32(3)
			expectedReplicasPerMaster := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "master-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", replicasPerMaster, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingDown", expectedReplicas, expectedReplicasPerMaster, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.ValidateRedisClusterMasterSlave(ctx, k8sClient, namespacedName, expectedReplicas, expectedReplicasPerMaster, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should insert data inside the cluster correctly", func() {
			rdclName := fmt.Sprintf("%s-%s", "data-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			replicas := int32(3)

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", 0, false, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.InsertDataIntoCluster(ctx, k8sClient, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			allPods := framework.GetPods(k8sClient, ctx, fetchedRedisCluster)

			Eventually(func() bool {
				isOk, err := framework.FlushClusterKeys(allPods)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should insert data & scaleUp the cluster correctly", func() {
			rdclName := fmt.Sprintf("%s-%s", "data-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			initialReplicas := int32(3)
			expectedReplicas := int32(5)

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, false, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.InsertDataIntoCluster(ctx, k8sClient, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			allPods := framework.GetPods(k8sClient, ctx, fetchedRedisCluster)

			Eventually(func() bool {
				isOk, err := framework.CheckClusterKeys(allPods)
				return isOk && err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.FlushClusterKeys(allPods)
				return isOk && err == nil
			}).Should(BeTrue())

		})

		It("Should insert data & scaleDown the cluster correctly", func() {
			rdclName := fmt.Sprintf("%s-%s", "data-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			initialReplicas := int32(5)
			expectedReplicas := int32(3)

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, false, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.InsertDataIntoCluster(ctx, k8sClient, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingDown", expectedReplicas, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			time.Sleep(40 * time.Second)

			allPods := framework.GetPods(k8sClient, ctx, fetchedRedisCluster)

			Eventually(func() bool {
				isOk, err := framework.CheckClusterKeys(allPods)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should change Resources from RedisCluster to StatefulSet", func() {
			objectsTestName := "resources-management"
			rdclName := fmt.Sprintf("%s-%s", objectsTestName, RedisClusterName)
			namespacedName := types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			initialReplicas := int32(1)
			expectedResources := corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("51m"),
					corev1.ResourceMemory: resource.MustParse("51Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("51m"),
					corev1.ResourceMemory: resource.MustParse("51Mi"),
				},
			}
			fetchedStatefulSet := &appsv1.StatefulSet{}

			framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Upgrading", 1, 0, true, &expectedResources, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedStatefulSet)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedStatefulSet.Spec.Template.Spec.Containers[0].Resources).To(Equal(expectedResources))

		})
		It("Should change Image from RedisCluster to StatefulSet", func() {
			objectsTestName := "resources-management"
			rdclName := fmt.Sprintf("%s-%s", objectsTestName, RedisClusterName)
			namespacedName := types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			initialReplicas := int32(1)
			expectedImage := "redis/redis-stack-server:7.2.0-v6"

			fetchedStatefulSet := &appsv1.StatefulSet{}

			err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Upgrading", 1, 0, true, nil, expectedImage, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err
			}, Timeout, PollingInterval).Should(Succeed())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedStatefulSet)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedStatefulSet.Spec.Template.Spec.Containers[0].Image).To(Equal(expectedImage))

		})
		It("Should not allow to change from ephemeral to storage", func() {
			initialStorage := string("1Gi")
			initialReplicas := int32(1)
			rdclName := fmt.Sprintf("%s-%s", "resources-management", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			// Try to scaleUp in redis-cluster
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterEphemeral(ctx, k8sClient, namespacedName, initialStorage, initialReplicas)
				return err == nil
			}).Should(BeFalse())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				return fetchedRedisCluster.Status.Status == "Ready"
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should Create and delete pdb from rediscluster", func() {
			// Creating redis cluster with PDB enabled should create it
			fetchedPDB := &pv1.PodDisruptionBudget{}
			pdb := redisv1.Pdb{
				Enabled: true,
			}
			pdb.PdbSizeAvailable.IntVal = int32(1)
			rdclName := fmt.Sprintf("%s-%s", "poddisruptionbudget", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}
			namespacedNameForPdb := types.NamespacedName{Name: rdclName + "-pdb", Namespace: RedisNamespace}
			initialReplicas := int32(2)

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, initialReplicas, "", 0, true, true, pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedPDB.Spec.MinAvailable.IntVal).To(Equal(int32(1)))

			// Disabling PDB should delete it
			pdb = redisv1.Pdb{
				Enabled: false,
			}
			pdb.PdbSizeAvailable.IntVal = int32(1)
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Ready", 2, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeFalse())

			// Re-enabling PDB should create it
			Expect(fetchedPDB.Spec.MinAvailable.IntVal).To(Equal(int32(1)))
			pdb = redisv1.Pdb{
				Enabled: true,
			}
			pdb.PdbSizeAvailable.IntVal = int32(1)
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Ready", 2, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedPDB.Spec.MinAvailable.IntVal).To(Equal(int32(1)))

			// Scaling down Redis cluster to 0 from 2 should delete the PDB
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "Ready", 0, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeFalse())

			// Scaling up Redis cluster to 2 from 0 should create the PDB
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", 2, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedPDB.Spec.MinAvailable.IntVal).To(Equal(int32(1)))

			// Scaling down Redis cluster to 1 from 2 should delete the PDB
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingDown", 1, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeFalse())

			// Scaling up Redis cluster to 2 from 1 should create the PDB
			Eventually(func() bool {
				_, err := framework.ChangeRedisClusterConfiguration(ctx, k8sClient, namespacedName, nil, "ScalingUp", 2, 0, true, nil, "", pdb, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			time.Sleep(20 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedNameForPdb, fetchedPDB)
				return err == nil
			}).Should(BeTrue())

			Expect(fetchedPDB.Spec.MinAvailable.IntVal).To(Equal(int32(1)))

			// Cleanup
			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should meet cluster nodes", func() {
			replicas := int32(3)
			rdclName := fmt.Sprintf("%s-%s", "cluster-meet", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			// wait for Ready status once cluster creation is completed
			time.Sleep(30 * time.Second)

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			// forget the first cluster node
			Eventually(func() bool {
				err := framework.ForgetANode(k8sClient, ctx, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			// wait for the operator next reconciliation loop
			time.Sleep(100 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err := k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should balance cluster", func() {
			replicas := int32(3)
			rdclName := fmt.Sprintf("%s-%s", "cluster-balance", RedisClusterName)
			namespacedName = types.NamespacedName{Name: rdclName, Namespace: RedisNamespace}

			Eventually(func() bool {
				err := framework.EnsureClusterExistsOrCreate(k8sClient, namespacedName, replicas, "", 0, true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			// Delete redis operator
			err := k8sClient.Delete(ctx, fetchedRedisClusterOperator)
			Expect(err).NotTo(HaveOccurred())

			// forget the first cluster node
			Eventually(func() bool {
				err := framework.ForgetANodeFixAndMeet(k8sClient, ctx, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			err = ensureOperatorExistsOrCreate(types.NamespacedName{
				Name:      "redis-operator",
				Namespace: RedisNamespace,
			})
			Expect(err).ToNot(HaveOccurred())

			redisClusterOperator, err := createOperator()
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: redisClusterOperator.Name, Namespace: RedisNamespace}, fetchedRedisClusterOperator)
				return err == nil
			}).Should(BeTrue())

			// wait for the operator next reconciliation loop and cluster rebalance
			time.Sleep(240 * time.Second)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				_, err := framework.WaitForReadyRdcl(ctx, k8sClient, namespacedName)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, namespacedName, fetchedRedisCluster)
				return err == nil
			}).Should(BeTrue())

			Eventually(func() bool {
				isOk, err := framework.CheckRedisCluster(k8sClient, ctx, fetchedRedisCluster)
				return isOk && err == nil
			}).Should(BeTrue())

			err = k8sClient.Delete(ctx, fetchedRedisCluster)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
