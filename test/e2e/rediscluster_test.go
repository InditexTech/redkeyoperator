// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/test/e2e/framework"
)

const (
	RedisClusterName = "rediscluster-test"
	version          = "6.0.2"
)

var (
	defaultSidecarImage = "alpine:3.1.2"
	changedRedisImage   = "redis/redis-stack-server:7.2.0-v10"
)

func getSidecarImage() string {
	if img := os.Getenv("SIDECARD_IMAGE"); img != "" {
		return img
	}
	return defaultSidecarImage
}

func getChangedRedisImage() string {
	if img := os.Getenv("CHANGED_REDIS_IMAGE"); img != "" {
		return img
	}
	return changedRedisImage
}

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
		name := rcList.Items[i].Name
		ns := rcList.Items[i].Namespace

		// Strip finalizers with retry
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			rc := &redisv1.RedisCluster{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, rc); err != nil {
				return err
			}
			rc.Finalizers = nil
			return c.Update(ctx, rc)
		})).To(Succeed(), "removing finalizers from %s/%s", ns, name)

		// delete the CR immediately
		Expect(c.Delete(ctx, &redisv1.RedisCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		})).To(Succeed(), "deleting RedisCluster %s/%s", ns, name)
	}

	// 2) Delete the namespace
	Expect(c.Delete(ctx, ns)).To(Succeed(), "deleting namespace %s", ns.Name)

	// 3) Wait for the namespace to actually disappear
	Eventually(func() bool {
		err := c.Get(ctx, types.NamespacedName{Name: ns.Name}, &corev1.Namespace{})
		return err != nil
	}, defaultWait, defaultPoll).Should(BeTrue(), "namespace %s should be gone", ns.Name)
}

var _ = Describe("Redis Operator & RedisCluster E2E", Label("operator", "cluster"), func() {
	var (
		namespace *corev1.Namespace
	)

	// mustCreateAndReady creates a cluster and blocks until it's Ready
	mustCreateAndReady := func(name string, replicas, replicasPerMaster int32, storage string, purgeKeys, ephemeral bool, pdb redisv1.Pdb, userOverride redisv1.RedisClusterOverrideSpec) *redisv1.RedisCluster {
		key := types.NamespacedName{Namespace: namespace.Name, Name: name}
		Expect(framework.EnsureClusterExistsOrCreate(
			ctx, k8sClient, key,
			replicas, replicasPerMaster,
			storage, purgeKeys, ephemeral,
			pdb, userOverride,
		)).To(Succeed())

		rc, err := framework.WaitForReady(ctx, k8sClient, key)

		Expect(err).NotTo(HaveOccurred())
		Expect(rc.Spec.Replicas).To(Equal(replicas))
		Expect(rc.Spec.Storage).To(Equal(storage))
		Expect(rc.Spec.PurgeKeysOnRebalance).To(Equal(purgeKeys))
		Expect(rc.Spec.Ephemeral).To(Equal(ephemeral))
		Expect(rc.Kind).To(Equal("RedisCluster"))
		Expect(rc.APIVersion).To(Equal("redis.inditex.com/v1"))
		Expect(rc.Spec.Auth).To(Equal(redisv1.RedisAuth{}))
		Expect(rc.Spec.Image).To(Equal(os.Getenv("REDIS_IMAGE")))
		Expect(rc.Spec.Pdb).To(Equal(pdb))
		Expect(rc.Spec.ReplicasPerMaster).To(Equal(replicasPerMaster))
		Expect(rc.Name).To(Equal(name))
		Expect(rc.Spec.Version).To(Equal(version))
		return rc
	}

	BeforeEach(func() {
		namespace = createNamespace(ctx, k8sClient, fmt.Sprintf("redis-e2e-%d", GinkgoParallelProcess()))
		Expect(EnsureOperatorSetup(ctx, namespace.Name)).To(Succeed())
	})
	AfterEach(func() {
		deleteNamespace(ctx, k8sClient, namespace)
	})

	Context("Operator health", func() {
		It("deploys and becomes healthy", func() {
			dep := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "redis-operator"}, dep)
				return err == nil && dep.Status.AvailableReplicas >= 1
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		})
	})

	Context("Lifecycle: create & scale", func() {
		const base = "rdcl"

		// helper: every phase in want must appear somewhere in trace
		expectPhases := func(trace []string, want ...string) {
			for _, p := range want {
				Expect(trace).To(ContainElement(p),
					"phase %q not present in trace %v", p, trace)
			}
		}

		DescribeTable("scale cycles",
			func(initial, target int32, phases []string) {
				// unique name per entry so jobs can run in parallel
				name := fmt.Sprintf("%s-%d-%d", base, initial, target)
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				mustCreateAndReady(name, initial, 0, "", true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

				// change replicas → wait Ready again
				rc, trace, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{
						Mutate: func(r *redisv1.RedisCluster) { r.Spec.Replicas = target },
					})
				Expect(err).NotTo(HaveOccurred())
				Expect(rc.Spec.Replicas).To(Equal(target))

				// every wanted phase must have shown up at least once
				expectPhases(trace, phases...)
			},

			Entry("up → 3", int32(0), int32(3),
				[]string{redisv1.StatusScalingUp, redisv1.StatusReady}),
			Entry("up → 5", int32(3), int32(5),
				[]string{redisv1.StatusScalingUp, redisv1.StatusReady}),
			Entry("down → 3", int32(5), int32(3),
				[]string{redisv1.StatusScalingDown, redisv1.StatusReady}),
			Entry("down → 0", int32(3), int32(0),
				[]string{redisv1.StatusReady}),
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
			initial     *redisv1.RedisClusterOverrideSpec
			update      *redisv1.RedisClusterOverrideSpec // nil → no update step
			validateSTS func(*appsv1.StatefulSet) bool    // after last step
		}

		entries := []entry{
			{
				name: "apply-tolerations-topology",
				initial: &redisv1.RedisClusterOverrideSpec{
					StatefulSet: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"testLabel": "true"},
							},
							Spec: corev1.PodSpec{
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
						redisv1.RedisClusterOverrideSpec{
							StatefulSet: &appsv1.StatefulSet{Spec: sts.Spec}, // only care it’s present
						})
				},
			},
			{
				name:    "add-side-car",
				initial: &redisv1.RedisClusterOverrideSpec{}, // start clean
				update: &redisv1.RedisClusterOverrideSpec{
					StatefulSet: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test-sidecar",
								Image:   getSidecarImage(),
								Command: []string{"sleep", "infinity"},
							}},
						}},
					}},
				},
				validateSTS: wantContainers(2),
			},
			{
				name: "remove-side-car",
				initial: &redisv1.RedisClusterOverrideSpec{
					StatefulSet: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test-sidecar",
								Image:   getSidecarImage(),
								Command: []string{"sleep", "infinity"},
							}},
						}},
					}},
				},
				update: &redisv1.RedisClusterOverrideSpec{
					StatefulSet: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: nil}},
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
					1, 0, "", true, true,
					redisv1.Pdb{},
					*e.initial,
				)

				// If an update override is defined, apply it now
				if e.update != nil {
					_, trace, err := framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{
							Mutate: func(r *redisv1.RedisCluster) { r.Spec.Override = e.update },
						})
					Expect(err).NotTo(HaveOccurred())
					Expect(trace).To(ContainElement(redisv1.StatusUpgrading))
					Expect(trace[len(trace)-1]).To(Equal(redisv1.StatusReady))
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

			// create cluster (3 replicas are enough)
			mustCreateAndReady(baseName, 3, 0, "", true, true,
				redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

			// helper to read *sorted* ports from the cluster-IP Service
			getPorts = func() []int32 {
				svc := &corev1.Service{}
				_ = k8sClient.Get(ctx, key, svc)
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
		test := func(mutator func(context.Context, client.Client, types.NamespacedName) error) {
			Expect(mutator(ctx, k8sClient, key)).To(Succeed())

			// eventually we must be back to a single comm port
			Eventually(getPorts, defaultWait*2, defaultPoll).
				Should(Equal([]int32{redis.RedisCommPort}))
		}

		DescribeTable("prunes / restores the *only* comm port",
			test,
			Entry("remove all ports", framework.RemoveServicePorts),
			Entry("add extra random ports", framework.AddServicePorts),
		)
	})

	Context("Spec.Labels propagation", func() {
		const clusterName = "spec-labels"
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: clusterName}
			mustCreateAndReady(
				clusterName,
				3, 0, // replicas / per-master
				"", true, true, // storage / purgeKeys / ephemeral
				redisv1.Pdb{},                      // no-PDB
				redisv1.RedisClusterOverrideSpec{}, // no override
			)
		})

		//-------------------------------------------------------------------- helpers

		checkRC := func(rc *redisv1.RedisCluster, applied map[string]string) {
			Expect(rc).NotTo(BeNil())
			Expect(rc.Status.Status).To(Equal(redisv1.StatusReady))
			Expect(rc.Spec.Replicas).To(Equal(int32(3))) // never touched by this test
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
			"redis-cluster-name":                    clusterName,
			"redis.rediscluster.operator/component": "redis",
		}
		with := map[string]string{
			"redis-cluster-name":                    clusterName,
			"redis.rediscluster.operator/component": "redis",
			"team":                                  "teamA",
			"foo":                                   "bar",
		}

		DescribeTable("adds / removes spec.labels",
			func(apply, want map[string]string) {
				rc, phases, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{
						Mutate: func(r *redisv1.RedisCluster) { r.Spec.Labels = &apply },
					})
				Expect(err).NotTo(HaveOccurred())

				// the returned RC is sane
				checkRC(rc, apply)

				// controller went Upgrading → Ready
				Expect(phases).To(ContainElements(redisv1.StatusUpgrading, redisv1.StatusReady))
				Expect(phases[len(phases)-1]).To(Equal(redisv1.StatusReady))

				// all children carry the wanted labels
				checkChildren(want)
			},

			Entry("set custom labels",
				map[string]string{"team": "teamA", "foo": "bar"}, // apply
				with, // wanted on children
			),
			Entry("clear labels",
				map[string]string{}, // apply (empty map)
				base,                // wanted on children
			),
		)
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
				true,       /* purgeKeys */
				false,      /* NOT ephemeral */
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)
		})

		checkRC := func(rc *redisv1.RedisCluster, wantReplicas int32) {
			Expect(rc).NotTo(BeNil())
			Expect(rc.Status.Status).To(Equal(redisv1.StatusReady))
			Expect(rc.Spec.Storage).To(Equal(initialPVC))
			Expect(rc.Spec.Replicas).To(Equal(wantReplicas))
		}

		// ---------------------------------------------------------------- table

		type tc struct {
			desc        string
			mutate      func(*redisv1.RedisCluster)
			wantRep     int32
			wantPhases  []string
			wantErrLike string
		}

		DescribeTable("PVC-backed cluster mutations",
			func(t tc) {
				rc, phases, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate})

				if t.wantErrLike != "" {
					// mutation must fail
					Expect(err).To(MatchError(ContainSubstring(t.wantErrLike)))
					// cluster should still be Ready with original spec
					rc, _ = framework.WaitForStatus(ctx, k8sClient, key, redisv1.StatusReady)
					checkRC(rc, initialRep)
					return
				}

				// mutation expected to succeed
				Expect(err).NotTo(HaveOccurred())
				checkRC(rc, t.wantRep)
				Expect(phases).To(ContainElements(t.wantPhases))
			},

			Entry("forbid storage resize",
				tc{
					desc: "resize PVC",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Storage = "1Gi"
					},
					wantRep:     initialRep,
					wantPhases:  nil, // not checked we expect an error
					wantErrLike: "Changing the storage size is not allowed",
				},
			),

			Entry("scale up to 6 replicas",
				tc{
					desc: "scale-up",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Replicas = 6
					},
					wantRep:    6,
					wantPhases: []string{redisv1.StatusScalingUp, redisv1.StatusReady},
				},
			),

			Entry("scale down to 1 replica",
				tc{
					desc: "scale-down",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Replicas = 1
					},
					wantRep:    1,
					wantPhases: []string{redisv1.StatusScalingDown, redisv1.StatusReady},
				},
			),
		)
	})

	Context("Master-Replica layout", func() {
		const (
			clusterName   = "master-test"
			initReplicas  = int32(5)
			initPerMaster = int32(1)
		)

		var key types.NamespacedName
		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: clusterName}

			// single bootstrap for every entry
			mustCreateAndReady(
				clusterName,
				initReplicas, initPerMaster,
				"",   // no PVC
				true, // purgeKeys
				true, // ephemeral
				redisv1.Pdb{},
				redisv1.RedisClusterOverrideSpec{},
			)
		})

		// ---------------------------------------------------------------- helpers

		checkLayout := func(rep, perMaster int32) {
			Eventually(func() (bool, error) {
				return framework.ValidateRedisClusterMasterSlave(
					ctx, k8sClient, key, rep, perMaster)
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		type tc struct {
			desc          string
			mutate        func(*redisv1.RedisCluster)
			wantRep       int32
			wantPerMaster int32
			wantPhases    []string
		}

		DescribeTable("master/replica mutations",
			func(t tc) {
				if t.mutate == nil {
					checkLayout(t.wantRep, t.wantPerMaster)
					return
				}

				rc, phases, err := framework.ChangeCluster(
					ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate},
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(rc.Spec.Replicas).To(Equal(t.wantRep))
				Expect(rc.Spec.ReplicasPerMaster).To(Equal(t.wantPerMaster))
				Expect(phases).To(ContainElements(t.wantPhases))

				checkLayout(t.wantRep, t.wantPerMaster)
			},

			// ──────────────────────────────────────────────────────────
			Entry("baseline distribution is correct",
				tc{
					desc:          "validateInitial",
					mutate:        nil,
					wantRep:       initReplicas,
					wantPerMaster: initPerMaster,
				},
			),

			Entry("scale up to 7/2",
				tc{
					desc: "scaleUp",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Replicas = 7
						r.Spec.ReplicasPerMaster = 2
					},
					wantRep:       7,
					wantPerMaster: 2,
					wantPhases:    []string{redisv1.StatusScalingUp, redisv1.StatusReady},
				},
			),

			Entry("scale down to 3/1",
				tc{
					desc: "scaleDown",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Replicas = 3
						r.Spec.ReplicasPerMaster = 1
					},
					wantRep:       3,
					wantPerMaster: 1,
					wantPhases:    []string{redisv1.StatusScalingDown, redisv1.StatusReady},
				},
			),
		)
	})

	Context("Data integrity across scale", func() {
		const base = "data-test"

		var (
			key types.NamespacedName
			rc  *redisv1.RedisCluster
		)

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: base}
			rc = mustCreateAndReady(base, 5, 0, "",
				/*purge*/ false /*ephemeral*/, true,
				redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
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
			replicas   int32 // target after mutate; 0 == "no scale"
			wantPhases []string
		}

		DescribeTable("insert data scales and check data persist",
			func(t tc) {
				insert()

				// optional scale
				if t.replicas > 0 {
					var trace []string
					var err error
					rc, trace, err = framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{
							Mutate: func(r *redisv1.RedisCluster) { r.Spec.Replicas = t.replicas },
						})
					Expect(err).NotTo(HaveOccurred())
					Expect(rc.Spec.Replicas).To(Equal(t.replicas))
					Expect(trace).To(ContainElements(t.wantPhases))
				}

				checkKeys()
				flush()
			},

			Entry("no scale",
				tc{replicas: 0},
			),

			Entry("scale from 5 → 7",
				tc{
					replicas:   7,
					wantPhases: []string{redisv1.StatusScalingUp, redisv1.StatusReady},
				},
			),

			Entry("scale from 5 → 3",
				tc{
					replicas:   3,
					wantPhases: []string{redisv1.StatusScalingDown, redisv1.StatusReady},
				},
			),
		)
	})

	Context("StatefulSet updates", func() {
		const name = "resources-test"
		var key types.NamespacedName

		BeforeEach(func() {
			key = types.NamespacedName{Namespace: namespace.Name, Name: name}
			mustCreateAndReady(name, 1, 0, "",
				true, true, redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
		})

		type tc struct {
			mutate     func(*redisv1.RedisCluster)
			verify     func(*appsv1.StatefulSet)
			wantPhases []string
		}

		DescribeTable("propagates to STS",
			func(t tc) {
				_, trace, err := framework.ChangeCluster(ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate})
				Expect(err).NotTo(HaveOccurred())
				Expect(trace).To(ContainElements(t.wantPhases))

				sts := &appsv1.StatefulSet{}
				Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
				t.verify(sts)
			},

			Entry("change resources",
				tc{
					mutate: func(r *redisv1.RedisCluster) {
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
					wantPhases: []string{redisv1.StatusUpgrading, redisv1.StatusReady},
				},
			),

			Entry("change image",
				tc{
					mutate: func(r *redisv1.RedisCluster) { r.Spec.Image = getChangedRedisImage() },
					verify: func(sts *appsv1.StatefulSet) {
						Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal(getChangedRedisImage()))
					},
					wantPhases: []string{redisv1.StatusUpgrading, redisv1.StatusReady},
				},
			),
		)
	})

	Context("Ephemeral → PVC guard", func() {
		const base = "ephemeral"

		type tc struct {
			desc      string
			mutate    func(*redisv1.RedisCluster)
			expectErr gomegatypes.GomegaMatcher
			verify    func(*redisv1.RedisCluster) // always run
		}

		DescribeTable("mutation attempts",
			func(t tc) {
				name := fmt.Sprintf("%s-%s",
					base, strings.ReplaceAll(strings.ToLower(t.desc), " ", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				mustCreateAndReady(name, 1, 0, "",
					/*purge=*/ true /*ephemeral=*/, true,
					redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

				_, _, err := framework.ChangeCluster(
					ctx, k8sClient, key,
					framework.ChangeClusterOptions{Mutate: t.mutate},
				)

				Expect(err).To(t.expectErr)

				rc, wErr := framework.WaitForStatus(ctx, k8sClient, key, redisv1.StatusReady)
				Expect(wErr).NotTo(HaveOccurred())
				t.verify(rc)
			},

			Entry("deny: flip to PVC (Ephemeral=false + Storage)",
				tc{
					desc: "flip-to-PVC",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Ephemeral = false
						r.Spec.Storage = "1Gi"
					},
					expectErr: MatchError(ContainSubstring("Changing the ephemeral field is not allowed")),
					verify: func(rc *redisv1.RedisCluster) {
						Expect(rc.Spec.Ephemeral).To(BeTrue())
						Expect(rc.Spec.Storage).To(BeEmpty())
					},
				},
			),

			Entry("deny: add Storage while Ephemeral=true",
				tc{
					desc: "add-storage",
					mutate: func(r *redisv1.RedisCluster) {
						r.Spec.Storage = "500Mi"
					},
					expectErr: MatchError(ContainSubstring("Ephemeral and storage cannot be combined")),
					verify: func(rc *redisv1.RedisCluster) {
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
			desc       string
			mutate     func(*redisv1.RedisCluster)
			wantPDB    bool
			wantPhases []string
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
				return (err == nil) == want
			}, defaultWait*2, defaultPoll).Should(BeTrue())
		}

		DescribeTable("reconciles PodDisruptionBudget according to .spec.pdb/enabled",
			func(t tc) {
				name := fmt.Sprintf("%s-%s", base, strings.ReplaceAll(t.name, "_", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}
				pdbKey := types.NamespacedName{Namespace: namespace.Name, Name: name + "-pdb"}

				mustCreateAndReady(name, 3, 0, "", true, true,
					redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

				for i, s := range t.steps {
					_, trace, err := framework.ChangeCluster(ctx, k8sClient, key,
						framework.ChangeClusterOptions{Mutate: s.mutate})
					Expect(err).NotTo(HaveOccurred(),
						"step %d (%s) ChangeCluster failed", i, s.desc)

					Expect(trace).To(ContainElements(s.wantPhases))

					ensurePDBState(pdbKey, s.wantPDB)
				}
			},

			Entry("enable → disable → re-enable",
				tc{
					name: "enable_disable_enable",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redisv1.RedisCluster) {
								r.Spec.Pdb = redisv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "disable PDB",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Pdb.Enabled = false },
							wantPDB:    false,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "re-enable PDB",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Pdb.Enabled = true },
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusReady},
						},
					},
				},
			),

			Entry("enable → scale-to-zero → scale-up",
				tc{
					name: "enable_scale_zero_up",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redisv1.RedisCluster) {
								r.Spec.Pdb = redisv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "scale to zero (PDB removed)",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Replicas = 0 },
							wantPDB:    false,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "scale up (PDB recreated)",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Replicas = 3 },
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusScalingUp, redisv1.StatusReady},
						},
					},
				},
			),

			Entry("enable → scale-to-zero → scale-up → disable",
				tc{
					name: "enable_scale_zero_up_disable",
					steps: []step{
						{
							desc: "enable PDB",
							mutate: func(r *redisv1.RedisCluster) {
								r.Spec.Pdb = redisv1.Pdb{Enabled: true, PdbSizeAvailable: intstr.FromInt(1)}
							},
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "scale to zero (PDB removed)",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Replicas = 0 },
							wantPDB:    false,
							wantPhases: []string{redisv1.StatusReady},
						},
						{
							desc:       "scale up (PDB recreated)",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Replicas = 3 },
							wantPDB:    true,
							wantPhases: []string{redisv1.StatusScalingUp, redisv1.StatusReady},
						},
						{
							desc:       "disable PDB again",
							mutate:     func(r *redisv1.RedisCluster) { r.Spec.Pdb.Enabled = false },
							wantPDB:    false,
							wantPhases: []string{redisv1.StatusReady},
						},
					},
				},
			),
		)
	})

	Context("Cluster healing", func() {
		const base = "heal" // prefix for all clusters

		scaleOperator := func(replicas int32) {
			depKey := types.NamespacedName{Namespace: namespace.Name, Name: "redis-operator"}
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
				return framework.CheckRedisCluster(k8sClient, ctx, rc)
			}, defaultWait*3, defaultPoll).Should(BeTrue())
		}

		type tc struct {
			desc     string                               // human-readable
			operate  func(rc *redisv1.RedisCluster) error // action that "breaks" the cluster
			preHook  func()                               // run before operate  (may be nil)
			postHook func()                               // run after  operate  (may be nil)
		}

		DescribeTable("self-heals after disruptive events",
			func(t tc) {
				// unique cluster name
				name := fmt.Sprintf("%s-%s",
					base, strings.ReplaceAll(strings.ToLower(t.desc), " ", "-"))
				key := types.NamespacedName{Namespace: namespace.Name, Name: name}

				// create a 3-master cluster
				rc := mustCreateAndReady(name,
					3 /*masters*/, 0, /*replicasPerMaster*/
					"" /*storage*/, true /*purgeKeys*/, true, /*ephemeral*/
					redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})

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
			Entry("forget one node - operator running",
				tc{
					desc: "forget-node",
					operate: func(rc *redisv1.RedisCluster) error {
						return framework.ForgetANode(k8sClient, ctx, rc)
					},
				},
			),

			// ────────────────── ENTRY 2 ──────────────────
			Entry("operator outage → forget/fix/meet → operator back",
				tc{
					desc:    "forget-fix-meet-without-operator",
					preHook: func() { scaleOperator(0) }, // stop the operator
					operate: func(rc *redisv1.RedisCluster) error {
						return framework.ForgetANodeFixAndMeet(k8sClient, ctx, rc)
					},
					postHook: func() { scaleOperator(1) }, // start it again
				},
			),
		)
	})
})
