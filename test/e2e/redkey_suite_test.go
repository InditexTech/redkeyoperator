// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	"github.com/inditextech/redkeyoperator/internal/redis"
	"github.com/inditextech/redkeyoperator/test/e2e/framework"
)

const (
	simpleTestDuration      = 5 * time.Minute
	rebalancingTestDuration = 10 * time.Minute
)

var _ = Describe("Redkey Operator & RedkeyCluster E2E", Label("operator", "cluster"), func() {
	var (
		namespace *corev1.Namespace
	)

	// mustCreateAndReady creates a cluster and blocks until it's Ready
	mustCreateAndReady := func(name string, primaries, replicasPerPrimary int32, storage, image string, purgeKeys, ephemeral bool, pdb redkeyv1.Pdb, userOverride redkeyv1.RedkeyClusterOverrideSpec) *redkeyv1.RedkeyCluster {
		key := types.NamespacedName{Namespace: namespace.Name, Name: name}

		err := framework.EnsureClusterExistsOrCreate(ctx, k8sClient, key, primaries, replicasPerPrimary,
			storage, image, purgeKeys, ephemeral, pdb, userOverride)

		Expect(err).To(Succeed())

		rc, err := framework.WaitForReady(ctx, k8sClient, key)

		Expect(err).NotTo(HaveOccurred())
		Expect(rc.Spec.Primaries).To(Equal(primaries))
		Expect(rc.Spec.Storage).To(Equal(storage))
		Expect(*rc.Spec.PurgeKeysOnRebalance).To(Equal(purgeKeys))
		Expect(rc.Spec.Ephemeral).To(Equal(ephemeral))
		Expect(rc.Spec.Auth).To(Equal(redkeyv1.RedisAuth{}))
		Expect(rc.Spec.Image).To(Equal(framework.GetRedisImage()))
		Expect(rc.Spec.Pdb).To(Equal(pdb))
		Expect(rc.Spec.ReplicasPerPrimary).To(Equal(replicasPerPrimary))
		Expect(rc.Name).To(Equal(name))
		return rc
	}

	BeforeEach(func() {
		var err error
		namespace, err = framework.CreateNamespace(ctx, k8sClient, fmt.Sprintf("redkey-e2e-%d", GinkgoParallelProcess()))
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "Error creating namespace: %e", err)
		}
		Expect(EnsureOperatorSetup(ctx, namespace.Name)).To(Succeed())
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "Error creating namespace: %e", err)
		}
	})
	AfterEach(func() {
		framework.DeleteNamespace(ctx, k8sClient, namespace)
	})

	Context("Operator health", func() {
		It("deploys and becomes healthy", func() {
			dep := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "redkey-operator"}, dep)
				return err == nil && dep.Status.AvailableReplicas >= 1
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})
	})

	Context("Lifecycle: create & scale", func() {
		const base = "rkcl"

		DescribeTable("scale cycles",
			func(ctx SpecContext, initial, target int32) {
				// unique name per entry so jobs can run in parallel
				name := fmt.Sprintf("%s-%d-%d", base, initial, target)
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				mustCreateAndReady(name, initial, 0, "", framework.GetRedisImage(), true, true, redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})

				// change primaries → wait Ready again
				rc, _, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{
						Mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = target },
					})
				Expect(err).NotTo(HaveOccurred())
				Expect(rc.Spec.Primaries).To(Equal(target))
			},

			Entry("scale up 0 → 3", SpecTimeout(simpleTestDuration), int32(0), int32(3)),
			Entry("scale up 3 → 5", SpecTimeout(simpleTestDuration), int32(3), int32(5)),
			Entry("scale down 5 → 3", SpecTimeout(simpleTestDuration), int32(5), int32(3)),
			Entry("scale down 3 → 0", SpecTimeout(simpleTestDuration), int32(3), int32(0)),
			Entry("scale up 0 → 3", SpecTimeout(simpleTestDuration), int32(0), int32(3)),
		)
	})

	Context("PodTemplate override", func() {
		const baseName = "override-test"
		var key types.NamespacedName

		// little helper: expect N containers in the STS template
		wantContainers := func(n int) func(*appsv1.StatefulSet) bool {
			return func(sts *appsv1.StatefulSet) bool {
				return len(sts.Spec.Template.Spec.Containers) == n
			}
		}

		type entry struct {
			name        string
			initial     *redkeyv1.RedkeyClusterOverrideSpec
			update      *redkeyv1.RedkeyClusterOverrideSpec // nil → no update step
			validateSTS func(*appsv1.StatefulSet) bool      // after last step
		}

		entries := []entry{
			{
				name: "apply-tolerations-topology",
				initial: &redkeyv1.RedkeyClusterOverrideSpec{
					StatefulSet: &redkeyv1.PartialStatefulSet{Spec: &redkeyv1.PartialStatefulSetSpec{
						Template: &redkeyv1.PartialPodTemplateSpec{
							Metadata: metav1.ObjectMeta{
								Labels: map[string]string{"testLabel": "true"},
							},
							Spec: redkeyv1.PartialPodSpec{
								Tolerations: []corev1.Toleration{{
									Key: "testToleration", Operator: corev1.TolerationOpExists,
									Effect: corev1.TaintEffectNoSchedule,
								}},
							},
						},
					}},
				},
				validateSTS: func(sts *appsv1.StatefulSet) bool {
					return framework.RedisStsContainsOverride(
						*sts,
						redkeyv1.RedkeyClusterOverrideSpec{
							StatefulSet: &redkeyv1.PartialStatefulSet{Spec: &redkeyv1.PartialStatefulSetSpec{}}, // only care it’s present
						})
				},
			},
			{
				name:    "add-side-car",
				initial: &redkeyv1.RedkeyClusterOverrideSpec{}, // start clean
				update: &redkeyv1.RedkeyClusterOverrideSpec{
					StatefulSet: &redkeyv1.PartialStatefulSet{Spec: &redkeyv1.PartialStatefulSetSpec{
						Template: &redkeyv1.PartialPodTemplateSpec{Spec: redkeyv1.PartialPodSpec{
							Containers: []corev1.Container{{
								Name:    "test-sidecar",
								Image:   framework.GetSidecarImage(),
								Command: []string{"sleep", "infinity"},
							}},
						}},
					}},
				},
				validateSTS: wantContainers(2),
			},
			{
				name: "remove-side-car",
				initial: &redkeyv1.RedkeyClusterOverrideSpec{
					StatefulSet: &redkeyv1.PartialStatefulSet{Spec: &redkeyv1.PartialStatefulSetSpec{
						Template: &redkeyv1.PartialPodTemplateSpec{Spec: redkeyv1.PartialPodSpec{
							Containers: []corev1.Container{{
								Name:    "test-sidecar",
								Image:   framework.GetSidecarImage(),
								Command: []string{"sleep", "infinity"},
							}},
						}},
					}},
				},
				update: &redkeyv1.RedkeyClusterOverrideSpec{
					StatefulSet: &redkeyv1.PartialStatefulSet{Spec: &redkeyv1.PartialStatefulSetSpec{
						Template: &redkeyv1.PartialPodTemplateSpec{Spec: redkeyv1.PartialPodSpec{Containers: nil}},
					}},
				},
				validateSTS: wantContainers(1),
			},
		}

		for _, e := range entries {
			e := e // pin
			It(fmt.Sprintf("handles %s", e.name), func() {
				key = types.NamespacedName{Namespace: namespace.Name, Name: baseName + "-" + strings.ReplaceAll(e.name, " ", "-")}

				// Create the cluster in one shot with initial override
				mustCreateAndReady(
					key.Name,
					1, 0, "", framework.GetRedisImage(), true, true,
					redkeyv1.Pdb{},
					*e.initial,
				)

				// If an update override is defined, apply it now
				if e.update != nil {
					_, _, err := framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{
							Mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Override = e.update },
						})
					Expect(err).NotTo(HaveOccurred())
				}

				// Final validation on the live StatefulSet
				sts := &appsv1.StatefulSet{}
				Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
				Expect(e.validateSTS(sts)).To(BeTrue())
			})
		}
	})

	Context("Service-ports reconciliation", func() {
		const baseName = "svc-ports"

		var key types.NamespacedName
		var getPorts func() []int32

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: baseName}

			// create cluster (3 primaries are enough)
			mustCreateAndReady(baseName, 3, 0, "", framework.GetRedisImage(), true, true,
				redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})

			// helper to read *sorted* ports from the cluster-IP Service
			getPorts = func() []int32 {
				svc := &corev1.Service{}
				err := k8sClient.Get(ctx, key, svc)
				if err != nil {
					fmt.Fprintf(GinkgoWriter, "Error getting PDB: %e", err)
				}
				ports := make([]int32, len(svc.Spec.Ports))
				for i, p := range svc.Spec.Ports {
					ports[i] = p.Port
				}
				slices.Sort(ports)
				return ports
			}

			// sanity-check: the operator always creates the comm port
			Expect(getPorts()).To(Equal([]int32{redis.RedisCommPort}))
		})

		// shared test-body: apply the mutator, then expect reconciliation
		test := func(ctx SpecContext, mutator func(context.Context, client.Client, types.NamespacedName) error) {
			Expect(mutator(ctx, k8sClient, key)).To(Succeed())

			// eventually we must be back to a single comm port
			Eventually(getPorts, defaultWait*2, defaultPoll).
				Should(Equal([]int32{redis.RedisCommPort}))
		}

		DescribeTable("prunes / restores the *only* comm port",
			test,
			Entry("remove all ports", SpecTimeout(simpleTestDuration), framework.RemoveServicePorts),
			Entry("add extra random ports", SpecTimeout(simpleTestDuration), framework.AddServicePorts),
		)
	})

	Context("Spec.Labels propagation", func() {
		const clusterName = "spec-labels"
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: clusterName}
			mustCreateAndReady(
				clusterName,
				3, 0, // primaries / replcias-per-primary
				"", framework.GetRedisImage(), true, true, // storage / image / purgeKeys / ephemeral
				redkeyv1.Pdb{},                       // no-PDB
				redkeyv1.RedkeyClusterOverrideSpec{}, // no override
			)
		})

		//-------------------------------------------------------------------- helpers

		checkRC := func(rc *redkeyv1.RedkeyCluster, applied map[string]string) {
			Expect(rc).NotTo(BeNil())
			Expect(rc.Status.Status).To(Equal(redkeyv1.StatusReady))
			Expect(rc.Spec.Primaries).To(Equal(int32(3))) // never touched by this test
			// .Spec.Labels must equal what we just applied
			Expect(*rc.Spec.Labels).To(Equal(applied))
		}

		checkChildren := func(expect map[string]string) {
			// StatefulSet
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
			Expect(sts.Spec.Template.Labels).To(Equal(expect))

			// ConfigMap
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, key, cm)).To(Succeed())
			Expect(cm.Labels).To(Equal(expect))

			// first Pod
			pod := &corev1.Pod{}
			podKey := types.NamespacedName{Name: clusterName + "-0", Namespace: key.Namespace}
			Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
			for k, v := range expect {
				Expect(pod.Labels[k]).To(Equal(v))
			}
		}

		base := map[string]string{
			"redkey-cluster-name":                    clusterName,
			"redis.redkeycluster.operator/component": "redis",
		}
		with := map[string]string{
			"redkey-cluster-name":                    clusterName,
			"redis.redkeycluster.operator/component": "redis",
			"team":                                   "teamA",
			"foo":                                    "bar",
		}

		It("sets and clears custom labels", func() {
			// Step 1: Set custom labels
			customLabels := map[string]string{"team": "teamA", "foo": "bar"}
			rc, _, err := framework.ChangeCluster(ctx, k8sClient, key,
				framework.ChangeClusterOptions{
					Mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Labels = &customLabels },
				})
			Expect(err).NotTo(HaveOccurred())

			// the returned RC is sane
			checkRC(rc, customLabels)

			// all children carry the wanted labels
			checkChildren(with)

			// Step 2: Clear labels (now there are custom labels to clear)
			emptyLabels := map[string]string{}
			rc, _, err = framework.ChangeCluster(ctx, k8sClient, key,
				framework.ChangeClusterOptions{
					Mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Labels = &emptyLabels },
				})
			Expect(err).NotTo(HaveOccurred())

			// the returned RC is sane
			checkRC(rc, emptyLabels)

			// all children carry only base labels
			checkChildren(base)
		})
	})

	Context("Storage guard & scaling", func() {
		const (
			name       = "storage-test"
			initialPVC = "500Mi"
			initialRep = int32(3)
		)
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: name}
			mustCreateAndReady(
				name,
				initialRep, 0,
				initialPVC, // with PVC
				framework.GetRedisImage(),
				false, /* NOT purgeKeys */
				false, /* NOT ephemeral */
				redkeyv1.Pdb{},
				redkeyv1.RedkeyClusterOverrideSpec{},
			)
		})

		checkRC := func(rc *redkeyv1.RedkeyCluster, wantPrimaries int32) {
			Expect(rc).NotTo(BeNil())
			Expect(rc.Status.Status).To(Equal(redkeyv1.StatusReady))
			Expect(rc.Spec.Storage).To(Equal(initialPVC))
			Expect(rc.Spec.Primaries).To(Equal(wantPrimaries))
		}

		// ---------------------------------------------------------------- table

		type tc struct {
			desc        string
			mutate      func(*redkeyv1.RedkeyCluster)
			wantRep     int32
			wantErrLike string
		}

		DescribeTable("PVC-backed cluster mutations",
			func(ctx SpecContext, t tc) {
				rc, _, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate})

				if t.wantErrLike != "" {
					// mutation must fail
					Expect(err).To(MatchError(ContainSubstring(t.wantErrLike)))
					// cluster should still be Ready with original spec
					rc, _ = framework.WaitForStatus(ctx, k8sClient, key, redkeyv1.StatusReady)
					checkRC(rc, initialRep)
					return
				}

				// mutation expected to succeed
				Expect(err).NotTo(HaveOccurred())
				checkRC(rc, t.wantRep)
			},

			Entry("forbid storage resize", SpecTimeout(simpleTestDuration),
				tc{
					desc: "resize PVC",
					mutate: func(r *redkeyv1.RedkeyCluster) {
						r.Spec.Storage = "1Gi"
					},
					wantRep:     initialRep,
					wantErrLike: "Changing the storage size is not allowed",
				},
			),

			Entry("scale up to 6 primaries", SpecTimeout(rebalancingTestDuration),
				tc{
					desc: "scale-up",
					mutate: func(r *redkeyv1.RedkeyCluster) {
						r.Spec.Primaries = 6
					},
					wantRep: 6,
				},
			),

			Entry("scale down to 1 primary", SpecTimeout(rebalancingTestDuration),
				tc{
					desc: "scale-down",
					mutate: func(r *redkeyv1.RedkeyCluster) {
						r.Spec.Primaries = 1
					},
					wantRep: 1,
				},
			),
		)
	})

	// Context("Primary-Replica layout", func() {
	// 	const (
	// 		clusterName    = "primary-test"
	// 		initPrimaries  = int32(3)
	// 		initPerPrimary = int32(1)
	// 	)

	// 	var key types.NamespacedName
	// 	BeforeEach(func() {
	// 		key = types.NamespacedName{Namespace: namespace.Name, Name: clusterName}

	// 		// single bootstrap for every entry
	// 		mustCreateAndReady(
	// 			clusterName,
	// 			initPrimaries, initPerPrimary,
	// 			"", // no PVC
	// 			framework.GetRedisImage(),
	// 			true, // purgeKeys
	// 			true, // ephemeral
	// 			redkeyv1.Pdb{},
	// 			redkeyv1.RedkeyClusterOverrideSpec{},
	// 		)
	// 	})

	// 	// ---------------------------------------------------------------- helpers

	// 	checkLayout := func(primaries, perPrimary int32) {
	// 		Eventually(func() (bool, error) {
	// 			return framework.ValidateRedkeyClusterPrimaryReplica(
	// 				ctx, k8sClient, key, primaries, perPrimary)
	// 		}, defaultWait*2, defaultPoll).Should(BeTrue())
	// 	}

	// 	type tc struct {
	// 		desc           string
	// 		mutate         func(*redkeyv1.RedkeyCluster)
	// 		wantRep        int32
	// 		wantPerPrimary int32
	// 	}

	// 	DescribeTable("primary/replica mutations",
	// 		func(ctx SpecContext, t tc) {
	// 			if t.mutate == nil {
	// 				checkLayout(t.wantRep, t.wantPerPrimary)
	// 				return
	// 			}

	// 			rc, _, err := framework.ChangeCluster(
	// 				ctx, k8sClient, key,
	// 				framework.ChangeClusterOptions{Mutate: t.mutate},
	// 			)
	// 			Expect(err).NotTo(HaveOccurred())
	// 			Expect(rc.Spec.Primaries).To(Equal(t.wantRep))
	// 			Expect(rc.Spec.ReplicasPerPrimary).To(Equal(t.wantPerPrimary))

	// 			checkLayout(t.wantRep, t.wantPerPrimary)
	// 		},

	// 		// ──────────────────────────────────────────────────────────
	// 		Entry("baseline distribution is correct", SpecTimeout(rebalancingTestDuration),
	// 			tc{
	// 				desc:           "validateInitial",
	// 				mutate:         nil,
	// 				wantRep:        initPrimaries,
	// 				wantPerPrimary: initPerPrimary,
	// 			},
	// 		),

	// 		Entry("scale up to 5/2", SpecTimeout(rebalancingTestDuration*2),
	// 			tc{
	// 				desc: "scaleUp",
	// 				mutate: func(r *redkeyv1.RedkeyCluster) {
	// 					r.Spec.Primaries = 5
	// 					r.Spec.ReplicasPerPrimary = 2
	// 				},
	// 				wantRep:        5,
	// 				wantPerPrimary: 2,
	// 			},
	// 		),

	// 		Entry("scale down to 3/1", SpecTimeout(rebalancingTestDuration*2),
	// 			tc{
	// 				desc: "scaleDown",
	// 				mutate: func(r *redkeyv1.RedkeyCluster) {
	// 					r.Spec.Primaries = 3
	// 					r.Spec.ReplicasPerPrimary = 1
	// 				},
	// 				wantRep:        3,
	// 				wantPerPrimary: 1,
	// 			},
	// 		),
	// 	)
	// })

	Context("Data integrity across scale", func() {
		const base = "data-test"

		var (
			key types.NamespacedName
			rc  *redkeyv1.RedkeyCluster
		)

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: base}
			rc = mustCreateAndReady(base, 5, 0, "", framework.GetRedisImage(),
				/*purge*/ false /*ephemeral*/, true,
				redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})
		})

		insert := func() {
			Eventually(func() error {
				ok, err := framework.InsertDataIntoCluster(ctx, k8sClient, key, rc)
				if !ok {
					return fmt.Errorf("insert error: %w", err)
				}
				return err
			}, defaultWait*2, defaultPoll).Should(Succeed())
		}

		checkKeys := func() {
			Eventually(func() (bool, error) {
				return framework.CheckClusterKeys(framework.GetPods(k8sClient, ctx, rc))
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		flush := func() {
			Eventually(func() (bool, error) {
				return framework.FlushClusterKeys(framework.GetPods(k8sClient, ctx, rc))
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		type tc struct {
			primaries int32 // target after mutate; 0 == "no scale"
		}

		DescribeTable("insert data scales and check data persist",
			func(ctx SpecContext, t tc) {
				insert()

				// optional scale
				if t.primaries > 0 {
					var err error
					rc, _, err = framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{
							Mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = t.primaries },
						})
					Expect(err).NotTo(HaveOccurred())
					Expect(rc.Spec.Primaries).To(Equal(t.primaries))
				}

				checkKeys()
				flush()
			},

			Entry("no scale", SpecTimeout(simpleTestDuration),
				tc{primaries: 0},
			),

			Entry("scale from 5 → 7", SpecTimeout(rebalancingTestDuration),
				tc{
					primaries: 7,
				},
			),

			Entry("scale from 5 → 3", SpecTimeout(rebalancingTestDuration),
				tc{
					primaries: 3,
				},
			),
		)
	})

	Context("StatefulSet updates", func() {
		const name = "resources-test"
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: name}
			mustCreateAndReady(name, 1, 0, "", framework.GetRedisImage(),
				true, true, redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})
		})

		type tc struct {
			mutate func(*redkeyv1.RedkeyCluster)
			verify func(*appsv1.StatefulSet)
		}

		DescribeTable("propagates to STS",
			func(ctx SpecContext, t tc) {
				_, _, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate})
				Expect(err).NotTo(HaveOccurred())

				sts := &appsv1.StatefulSet{}
				Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
				t.verify(sts)
			},

			Entry("change resources", SpecTimeout(simpleTestDuration),
				tc{
					mutate: func(r *redkeyv1.RedkeyCluster) {
						req := corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("51m"),
								corev1.ResourceMemory: resource.MustParse("51Mi")},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("51m"),
								corev1.ResourceMemory: resource.MustParse("51Mi")},
						}
						r.Spec.Resources = &req
					},
					verify: func(sts *appsv1.StatefulSet) {
						Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("51m"))
						Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("51Mi"))
						Expect(sts.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("51m"))
						Expect(sts.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("51Mi"))
					},
				},
			),

			Entry("change image", SpecTimeout(simpleTestDuration),
				tc{
					mutate: func(r *redkeyv1.RedkeyCluster) { r.Spec.Image = framework.GetChangedRedisImage() },
					verify: func(sts *appsv1.StatefulSet) {
						Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal(framework.GetChangedRedisImage()))
					},
				},
			),
		)
	})

	Context("Ephemeral → PVC guard", func() {
		const base = "ephemeral"

		type tc struct {
			desc      string
			mutate    func(*redkeyv1.RedkeyCluster)
			expectErr gomegatypes.GomegaMatcher
			verify    func(*redkeyv1.RedkeyCluster) // always run
		}

		DescribeTable("mutation attempts",
			func(ctx SpecContext, t tc) {
				name := fmt.Sprintf("%s-%s",
					base, strings.ReplaceAll(strings.ToLower(t.desc), " ", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				mustCreateAndReady(name, 1, 0, "", framework.GetRedisImage(),
					/*purge=*/ true /*ephemeral=*/, true,
					redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})

				_, _, err := framework.ChangeCluster(
					ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate},
				)

				Expect(err).To(t.expectErr)

				rc, wErr := framework.WaitForStatus(ctx, k8sClient, key, redkeyv1.StatusReady)
				Expect(wErr).NotTo(HaveOccurred())
				t.verify(rc)
			},

			Entry("deny: flip to PVC (Ephemeral=false + Storage)", SpecTimeout(simpleTestDuration),
				tc{
					desc: "flip-to-PVC",
					mutate: func(r *redkeyv1.RedkeyCluster) {
						r.Spec.Ephemeral = false
						r.Spec.Storage = "1Gi"
					},
					expectErr: MatchError(ContainSubstring("Changing the ephemeral field is not allowed")),
					verify: func(rc *redkeyv1.RedkeyCluster) {
						Expect(rc.Spec.Ephemeral).To(BeTrue())
						Expect(rc.Spec.Storage).To(BeEmpty())
					},
				},
			),

			Entry("deny: add Storage while Ephemeral=true", SpecTimeout(simpleTestDuration),
				tc{
					desc: "add-storage",
					mutate: func(r *redkeyv1.RedkeyCluster) {
						r.Spec.Storage = "500Mi"
					},
					expectErr: MatchError(ContainSubstring("Ephemeral and storage cannot be combined")),
					verify: func(rc *redkeyv1.RedkeyCluster) {
						Expect(rc.Spec.Ephemeral).To(BeTrue())
						Expect(rc.Spec.Storage).To(BeEmpty())
					},
				},
			),
		)
	})

	Context("PodDisruptionBudget toggle", func() {
		const base = "pdb"

		type step struct {
			desc    string
			mutate  func(*redkeyv1.RedkeyCluster)
			wantPDB bool
		}

		type tc struct {
			name  string
			steps []step
		}

		getPDB := func(pdbKey types.NamespacedName) error {
			return k8sClient.Get(ctx, pdbKey, &policyv1.PodDisruptionBudget{})
		}
		ensurePDBState := func(pdbKey types.NamespacedName, want bool) {
			Eventually(func() bool {
				err := getPDB(pdbKey)
				if (want && err == nil) || (!want && errors.IsNotFound(err)) {
					return true
				}
				if err != nil {
					fmt.Fprintf(GinkgoWriter, "Error getting PDB: %s: %s\n", pdbKey, err)
				}
				return false
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		DescribeTable("reconciles PodDisruptionBudget according to .spec.pdb/enabled",
			func(ctx SpecContext, t tc) {
				name := fmt.Sprintf("%s-%s", base, strings.ReplaceAll(t.name, "_", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}
				pdbKey := types.NamespacedName{Namespace: namespace.Name, Name: name + "-pdb"}
				mustCreateAndReady(name, 3, 0, "", framework.GetRedisImage(), true, true,
					redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})

				for i, s := range t.steps {
					_, _, err := framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{Mutate: s.mutate})
					Expect(err).NotTo(HaveOccurred(),
						"step %d (%s) ChangeCluster failed", i, s.desc)

					ensurePDBState(pdbKey, s.wantPDB)
				}
			},

			Entry("enable → disable → re-enable", SpecTimeout(simpleTestDuration),
				tc{
					name: "enable_disable_enable",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redkeyv1.RedkeyCluster) {
								r.Spec.Pdb = redkeyv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB: true,
						},
						{
							desc:    "disable PDB",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Pdb.Enabled = false },
							wantPDB: false,
						},
						{
							desc:    "re-enable PDB",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Pdb.Enabled = true },
							wantPDB: true,
						},
					},
				},
			),

			Entry("enable → scale-to-zero → scale-up", SpecTimeout(simpleTestDuration),
				tc{
					name: "enable_scale_zero_up",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redkeyv1.RedkeyCluster) {
								r.Spec.Pdb = redkeyv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB: true,
						},
						{
							desc:    "scale to zero (PDB removed)",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = 0 },
							wantPDB: false,
						},
						{
							desc:    "scale up (PDB recreated)",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = 3 },
							wantPDB: true,
						},
					},
				},
			),

			Entry("enable → scale-to-zero → scale-up → disable", SpecTimeout(simpleTestDuration),
				tc{
					name: "enable_scale_zero_up_disable",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redkeyv1.RedkeyCluster) {
								r.Spec.Pdb = redkeyv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB: true,
						},
						{
							desc:    "scale to zero (PDB removed)",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = 0 },
							wantPDB: false,
						},
						{
							desc:    "scale up (PDB recreated)",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Primaries = 3 },
							wantPDB: true,
						},
						{
							desc:    "disable PDB again",
							mutate:  func(r *redkeyv1.RedkeyCluster) { r.Spec.Pdb.Enabled = false },
							wantPDB: false,
						},
					},
				},
			),
		)
	})

	Context("Cluster healing", func() {
		const base = "heal" // prefix for all clusters

		scaleOperator := func(replicas int32) {
			depKey := types.NamespacedName{Namespace: namespace.Name, Name: "redkey-operator"}
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, depKey, dep)).To(Succeed())

			dep.Spec.Replicas = ptr.To(replicas)
			Expect(k8sClient.Update(ctx, dep)).To(Succeed())

			Eventually(func() bool {
				var d appsv1.Deployment
				if err := k8sClient.Get(ctx, depKey, &d); err != nil {
					return false
				}
				if replicas == 0 {
					return d.Status.AvailableReplicas == 0
				}
				return d.Status.AvailableReplicas >= replicas
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		waitHealthy := func(key types.NamespacedName) {
			Eventually(func() (bool, error) {
				rc, err := framework.WaitForReady(ctx, k8sClient, key)
				if err != nil {
					return false, err
				}
				return framework.CheckRedkeyCluster(k8sClient, ctx, rc)
			}, defaultWait*3, defaultPoll).Should(BeTrue())
		}

		type tc struct {
			desc     string                                 // human-readable
			operate  func(rc *redkeyv1.RedkeyCluster) error // action that "breaks" the cluster
			preHook  func()                                 // run before operate  (may be nil)
			postHook func()                                 // run after  operate  (may be nil)
		}

		DescribeTable("self-heals after disruptive events",
			func(ctx SpecContext, t tc) {
				// unique cluster name
				name := fmt.Sprintf("%s-%s",
					base, strings.ReplaceAll(strings.ToLower(t.desc), " ", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				// create a 3-primaries cluster
				rc := mustCreateAndReady(name,
					3 /*primaries*/, 0, /*replicasPerPrimary*/
					"" /*storage*/, framework.GetRedisImage(), true /*purgeKeys*/, true, /*ephemeral*/
					redkeyv1.Pdb{}, redkeyv1.RedkeyClusterOverrideSpec{})

				// optional scale operator
				if t.preHook != nil {
					t.preHook()
				}

				// perform the disruptive operation
				Expect(t.operate(rc)).To(Succeed())

				// optional return to previous phase operator
				if t.postHook != nil {
					t.postHook()
				}

				// eventually the cluster must be healthy again
				waitHealthy(key)
			},

			// ────────────────── ENTRY 1 ──────────────────
			Entry("forget one node - operator running", SpecTimeout(simpleTestDuration),
				tc{
					desc: "forget-node",
					operate: func(rc *redkeyv1.RedkeyCluster) error {
						return framework.ForgetANode(k8sClient, ctx, rc)
					},
				},
			),

			// ────────────────── ENTRY 2 ──────────────────
			Entry("operator outage → forget/fix/meet → operator back", SpecTimeout(simpleTestDuration),
				tc{
					desc:    "forget-fix-meet-without-operator",
					preHook: func() { scaleOperator(0) }, // stop the operator
					operate: func(rc *redkeyv1.RedkeyCluster) error {
						return framework.ForgetANodeFixAndMeet(k8sClient, ctx, rc)
					},
					postHook: func() { scaleOperator(1) }, // start it again
				},
			),
		)
	})
})
