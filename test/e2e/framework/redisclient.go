// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/redis"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// -- defaults & constants --

const (
	DBSIZECMD              string = "redis-cli DBSIZE"
	FLUSHCMD               string = "redis-cli FLUSHALL"
	CLUSTERINFOCMD         string = "redis-cli cluster info"
	CLUSTERNODESCMD        string = "redis-cli cluster nodes"
	EXPECTEDKEYS           int    = 30
	WAIT_FOR_RDCL_DELETION int    = 180
	defaultConfig                 = `save ""
appendonly no
maxmemory 70mb`
	finalizerName  = "redis.inditex.com/configmap-cleanup"
	pollInterval   = 3 * time.Second
	defaultTimeout = 30 * time.Minute
)

type ClusterStatus struct {
	State                 string
	SlotsOk               string
	ClusterSize           string
	KnownNodes            string
	StatsMessagesSent     string
	StatsMessagesReceived string
	NodeIps               string
}

var version = "6.0.2"

// EnsureClusterExistsOrCreate will create or update (upsert) a RedisCluster CR.
// It applies storage, replica count, PDB and optional overrides, then waits for reconciliation.
func EnsureClusterExistsOrCreate(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	replicas, replicasPerMaster int32,
	storage, image string,
	purgeKeys, ephemeral bool,
	pdb redisv1.Pdb,
	userOverride redisv1.RedisClusterOverrideSpec,
) error {
	rc := &redisv1.RedisCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{"team": "team-a"},
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, rc, func() error {
		// Base spec
		rc.Spec = redisv1.RedisClusterSpec{
			Auth:                 redisv1.RedisAuth{},
			Version:              version,
			Replicas:             replicas,
			Ephemeral:            ephemeral,
			Image:                image,
			Config:               defaultConfig,
			Resources:            buildResources(),
			PurgeKeysOnRebalance: purgeKeys,
		}

		// Storage override
		if storage != "" {
			rc.Spec.DeletePVC = true
			rc.Spec.Ephemeral = false
			rc.Spec.Storage = storage
			rc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		}

		// PDB override
		if !reflect.DeepEqual(pdb, redisv1.Pdb{}) {
			rc.Spec.Pdb = pdb
		}

		// Replicas‑per‑master override
		if replicasPerMaster > 0 {
			rc.Spec.ReplicasPerMaster = replicasPerMaster
		}

		// Start with the user’s override (if any), or an empty one
		var ov redisv1.RedisClusterOverrideSpec
		if userOverride.StatefulSet != nil || userOverride.Service != nil {
			ov = userOverride
		}

		// Now ensure the non‑root security context is present on the StatefulSet template:
		if ov.StatefulSet == nil {
			ov.StatefulSet = &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{},
				},
			}
		}
		// Merge in SecurityContext
		podSpec := &ov.StatefulSet.Spec.Template.Spec
		if podSpec.SecurityContext == nil {
			podSpec.SecurityContext = &corev1.PodSecurityContext{}
		}
		podSpec.SecurityContext.RunAsNonRoot = ptr.To(true)
		podSpec.SecurityContext.RunAsUser = ptr.To(int64(1001))
		podSpec.SecurityContext.RunAsGroup = ptr.To(int64(1001))
		podSpec.SecurityContext.FSGroup = ptr.To(int64(1001))

		// Finally attach the override
		rc.Spec.Override = &ov

		return nil
	})
	if err != nil {
		return fmt.Errorf("upsert RedisCluster %s/%s: %w", key.Namespace, key.Name, err)
	}
	return nil
}

// buildResources returns the default resource requirements for Redis pods.
func buildResources() *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}

// WaitForStatus blocks until .Status.Status equals desiredStatus **or Ready**.
// If desiredStatus itself is "Ready" we obviously require an exact match.
func WaitForStatus(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	desired string,
) (*redisv1.RedisCluster, error) {

	const interval = 3 * time.Second
	var last string

	isDone := func(s string) bool {
		if s == desired {
			return true
		}
		// if we were waiting for a transient state and the controller already
		// completed the reconciliation, Ready is also acceptable.
		return desired != redisv1.StatusReady && s == redisv1.StatusReady
	}

	if err := wait.PollUntilContextTimeout(
		ctx, interval, defaultTimeout, true,
		func(ctx context.Context) (bool, error) {
			rc := &redisv1.RedisCluster{}
			if err := c.Get(ctx, key, rc); err != nil {
				// keep polling if NotFound, abort on any other error
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			last = rc.Status.Status
			return isDone(last), nil
		},
	); err != nil {
		return nil, fmt.Errorf(
			"timed out after %s waiting for status %q (last seen %q): %w",
			defaultTimeout, desired, last, err,
		)
	}

	// fetch the fresh object before returning
	final := &redisv1.RedisCluster{}
	if err := c.Get(ctx, key, final); err != nil {
		return nil, fmt.Errorf("fetch latest RedisCluster %s/%s: %w",
			key.Namespace, key.Name, err)
	}
	return final, nil
}

// Convenience wrapper for waiting until “Ready”
func WaitForReady(ctx context.Context, c client.Client, key types.NamespacedName) (*redisv1.RedisCluster, error) {
	return WaitForStatus(ctx, c, key, redisv1.StatusReady)
}

// ChangeCluster mutates the RedisCluster specified by key:
//
//  1. wait until the cluster is Ready
//  2. run opts.Mutate (wrapped in controller-runtime CreateOrUpdate +
//     RetryOnConflict)
//  3. wait again until the controller has observed *that* generation
//     and set the phase back to Ready
//
// It returns the up-to-date object *and* the list of distinct phases that
// were observed while waiting (useful for asserting “ScalingUp” / “Upgrading”
// etc. occurred).
type ChangeClusterOptions struct {
	Mutate func(rc *redisv1.RedisCluster)
}

func ChangeCluster(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	opts ChangeClusterOptions,
) (*redisv1.RedisCluster, []string, error) {
	// 1) ensure initial Ready
	if _, err := WaitForStatus(ctx, c, key, redisv1.StatusReady); err != nil {
		return nil, nil, err
	}

	// 2) mutate (with retry-on-conflict)
	rc := &redisv1.RedisCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := controllerutil.CreateOrUpdate(ctx, c, rc, func() error {
			opts.Mutate(rc)
			controllerutil.AddFinalizer(rc, finalizerName) // keep your existing finalizer
			return nil
		})
		return err
	}); err != nil {
		return nil, nil, fmt.Errorf("patch RedisCluster: %w", err)
	}

	// 3) re-get to know the generation we just wrote
	if err := c.Get(ctx, key, rc); err != nil {
		return nil, nil, err
	}
	wantGen := rc.Generation

	// 4) wait for Ready again and capture the status trace
	ready, trace, err := waitForReady(ctx, c, key, wantGen)
	return ready, trace, err
}

// WaitForReadyWithTrace is just a convenience wrapper used by tests that need
// the trace list.  If you only need the final object, call waitForReady
// directly.
func WaitForReadyWithTrace(
	ctx context.Context,
	cl client.Client,
	key types.NamespacedName,
	wantGen int64,
) (*redisv1.RedisCluster, []string, error) {
	return waitForReady(ctx, cl, key, wantGen)
}

// waitForReady blocks until the cluster is Ready *and* the reconciler has
// observed at least generation >= wantGen.  It also returns a slice with the
// phases it saw (useful in tests to assert intermediary states).
func waitForReady(
	ctx context.Context,
	cl client.Client,
	key types.NamespacedName,
	wantGen int64,
) (*redisv1.RedisCluster, []string, error) {

	var (
		trace      []string
		last       string
		readyCount int
		rc         = &redisv1.RedisCluster{}
	)

	deadline := time.After(defaultTimeout)

	for {
		select {
		case <-ctx.Done():
			return nil, trace, ctx.Err()
		case <-deadline:
			return nil, trace,
				fmt.Errorf("timeout waiting Ready, seen phases %v", trace)
		default:
			time.Sleep(pollInterval)
		}

		if err := cl.Get(ctx, key, rc); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return nil, trace, err
		}

		phase := rc.Status.Status
		gen := readyObservedGen(rc)

		// record phase transitions (for debugging / assertions)
		if phase != last {
			// log.Printf("[waitForReady] phase=%s generation=%d wantGen=%d", phase, gen, wantGen)
			trace = append(trace, phase)
			last = phase
		}

		// consecutive-Ready logic
		if phase == redisv1.StatusReady && gen >= wantGen {
			readyCount++
			if readyCount == 2 { // <- stable Ready
				return rc.DeepCopy(), trace, nil
			}
		} else {
			readyCount = 0 // any other phase resets the counter
		}
	}
}

// readyObservedGen returns the generation the controller has already
// observed.  ❶ prefer .status.observedGeneration; ❷ fall back to the
// Ready condition; ❸ if both are zero, just return rc.Generation so
// the waiter can still progress.
func readyObservedGen(rc *redisv1.RedisCluster) int64 {
	for _, c := range rc.Status.Conditions {
		if c.Type == redisv1.StatusReady {
			return c.ObservedGeneration // <- the only reliable place
		}
	}
	return rc.Generation // fallback
}

func CheckRedisCluster(k8Client client.Client, ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	allPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	k8Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})

	rdclStatus, err := inspectRedisClusterStatus(allPods)

	if err != nil {
		return false, err
	}

	isOkStatus := checkRedisClusterConditions(rdclStatus)

	return isOkStatus, nil
}

func GetPods(k8Client client.Client, ctx context.Context, redisCluster *redisv1.RedisCluster) *corev1.PodList {
	allPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	k8Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})

	return allPods
}

func RedisStsContainsOverride(sts appsv1.StatefulSet, override redisv1.RedisClusterOverrideSpec) bool {
	// Labels and annotations in override must exist and have the same content in sts
	for k, v := range override.StatefulSet.Spec.Template.Labels {
		if sts.Spec.Template.Labels[k] != v {
			ctrl.Log.Error(fmt.Errorf("label %v not equal in sts: %v and override: %v", k, sts.Spec.Template.Labels[k], v), "Error")
			return false
		}
	}

	for k, v := range override.StatefulSet.Spec.Template.Annotations {
		if sts.Spec.Template.Annotations[k] != v {
			ctrl.Log.Error(fmt.Errorf("annotation %v not equal in sts: %v and override: %v", k, sts.Spec.Template.Annotations[k], v), "Error")
			return false
		}
	}

	if !reflect.DeepEqual(sts.Spec.Template.Spec.Tolerations, override.StatefulSet.Spec.Template.Spec.Tolerations) {
		ctrl.Log.Error(fmt.Errorf("taints not equal in sts: %v and override: %v", sts.Spec.Template.Spec.Tolerations, override.StatefulSet.Spec.Template.Spec.Tolerations), "Error")
		return false
	}

	if !reflect.DeepEqual(sts.Spec.Template.Spec.TopologySpreadConstraints, override.StatefulSet.Spec.Template.Spec.TopologySpreadConstraints) {
		ctrl.Log.Error(fmt.Errorf("topology not equal in sts: %v and override: %v", sts.Spec.Template.Spec.TopologySpreadConstraints, override.StatefulSet.Spec.Template.Spec.TopologySpreadConstraints), "Error")
		return false
	}

	return true
}

func createAndInsertDataIntoCluster(pods *corev1.PodList) error {
	var cmd string
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found")
	}

	pod := pods.Items[0]

	err := waitForContainerReady(pod)
	if err != nil {
		return err
	}

	for i := 0; i < EXPECTEDKEYS; i++ {
		key := fmt.Sprintf("%v-%v", randomValue(), randomValue())
		value := fmt.Sprintf("%v-%v", randomValue(), randomValue())
		cmd = fmt.Sprintf("redis-cli -c set %v %v", key, value)
		_, _, err := remoteCommand(pod.Namespace, pod.Name, cmd)
		if err != nil {
			continue
		}
		time.Sleep(1 * time.Second)
	}

	return nil
}

// waitForContainerReady checks if the container in a pod is ready within a specified timeout period.
func waitForContainerReady(pod corev1.Pod) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return wait.PollUntilContextTimeout(ctx, 1*time.Second, 300*time.Second, true, func(ctx context.Context) (bool, error) {
		if isContainerReady(pod) {
			return true, nil
		}
		return false, nil
	})
}

// isContainerReady checks if any container in the pod is ready.
func isContainerReady(pod corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Ready {
			return true
		}
	}
	return false
}

func CheckClusterKeys(pods *corev1.PodList) (bool, error) {
	if len(pods.Items) == 0 {
		return false, fmt.Errorf("no pods found")
	}
	var totalKeys int

	for _, pod := range pods.Items {
		time.Sleep(5 * time.Second)
		value, _, err := remoteCommand(pod.Namespace, pod.Name, DBSIZECMD)
		if err != nil {
			return false, err
		}
		time.Sleep(1 * time.Second)

		formatValue := strings.TrimSpace(value)
		intValue, err := strconv.Atoi(formatValue)
		if err != nil {
			return false, err
		}
		fmt.Printf("")
		totalKeys += intValue
	}
	return totalKeys == EXPECTEDKEYS, nil
}

func FlushClusterKeys(pods *corev1.PodList) (bool, error) {
	if len(pods.Items) == 0 {
		return false, fmt.Errorf("no pods found")
	}
	var totalKeys int

	for _, pod := range pods.Items {
		time.Sleep(1 * time.Second)
		_, _, err := remoteCommand(pod.Namespace, pod.Name, FLUSHCMD)
		if err != nil {
			return false, err
		}
		time.Sleep(1 * time.Second)

		value, _, err := remoteCommand(pod.Namespace, pod.Name, DBSIZECMD)
		if err != nil {
			return false, err
		}
		time.Sleep(1 * time.Second)

		formatValue := strings.TrimSpace(value)
		intValue, err := strconv.Atoi(formatValue)
		if err != nil {
			return false, err
		}

		totalKeys += intValue
	}
	return totalKeys == 0, nil
}

func randomValue() string {
	// Create a private random number generator with a specific seed
	randomGenerator := rand.New(rand.NewSource(time.Now().UnixNano()))
	// Generate a random number between 0 and 100
	randomNumber := fmt.Sprintf("%v", randomGenerator.Intn(100))
	return randomNumber
}

func inspectRedisClusterStatus(pods *corev1.PodList) (ClusterStatus, error) {
	var cmd string
	if len(pods.Items) == 0 {
		return ClusterStatus{}, fmt.Errorf("no pods found")
	}

	pod := pods.Items[0]
	cmd = fmt.Sprintf("%s; %s", CLUSTERINFOCMD, CLUSTERNODESCMD)
	stdOut, _, err := remoteCommand(pod.Namespace, pod.Name, cmd)
	if err != nil {
		return ClusterStatus{}, err
	}

	clusterStatus := getStatusFromStdOut(stdOut)
	return clusterStatus, nil
}

func checkRedisClusterConditions(clusterStatus ClusterStatus) bool {
	statusOK := true

	if strings.ToLower(clusterStatus.State) != "ok" {
		return false
	}

	return statusOK
}

// getStatusFromStdOut parses the output string and apply regex to get information about the cluster status,
// number of slots ok, known nodes, cluster size, messages sent and messages received
func getStatusFromStdOut(stdOut string) ClusterStatus {
	var clusterState string
	re := regexp.MustCompile(`(?m)^[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_state:(?P<clusterState>[a-zA-Z]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_slots_ok:(?P<clusterSlotsOk>[0-9]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_known_nodes:(?P<clusterKnownNodes>[0-9]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_size:(?P<clusterSize>[0-9]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_stats_messages_sent:(?P<clusterStatsMessagesSent>[0-9]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*cluster_stats_messages_received:(?P<clusterStatsMessagesReceived>[0-9]*)[a-zA-Z0-9\n_: .\-,\t\[\]\r]*$`)
	match := re.FindStringSubmatch(stdOut)
	result := make(map[string]string)
	if len(match) != 0 {
		for i, name := range re.SubexpNames() {
			if i != 0 && name != "" && len(match) != 0 {
				result[name] = match[i]
			}
		}
		clusterState = result["clusterState"]
	} else {
		if strings.Contains(stdOut, "cluster support disabled") {
			clusterState = "disabled"
		}
	}

	nodeIps := getNodeIPsFromStdOut(stdOut)

	var clusterStatus = ClusterStatus{
		State:                 clusterState,
		SlotsOk:               result["clusterSlotsOk"],
		ClusterSize:           result["clusterSize"],
		KnownNodes:            result["clusterKnownNodes"],
		NodeIps:               nodeIps,
		StatsMessagesSent:     result["clusterStatsMessagesSent"],
		StatsMessagesReceived: result["clusterStatsMessagesReceived"],
	}
	return clusterStatus
}

// getNodeIPsFromStdOut parses the output string and apply regex to get the ips of the nodes
func getNodeIPsFromStdOut(stdOut string) string {
	var nodeIPs string

	var re = regexp.MustCompile(`(?m)([\d]{1,3}\.[\d]{1,3}\.[\d]{1,3}\.[\d]{1,3})`)
	for _, match := range re.FindAllString(stdOut, -1) {
		if len(nodeIPs) == 0 {
			nodeIPs = match
		} else {
			nodeIPs = fmt.Sprintf("%s; %s", nodeIPs, match)
		}
	}
	return nodeIPs
}

// ValidateRedisClusterMasterSlave waits until Ready, then checks that
// the number of masters & replicas-per-master match, and the StatefulSet
// has the correct total replica count.
func ValidateRedisClusterMasterSlave(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	replicas, replicasPerMaster int32,
) (bool, error) {
	// Wait until .Status == "Ready"
	rc, err := WaitForReady(ctx, c, key)
	if err != nil {
		return false, err
	}

	// Now rc.Spec.Replicas and rc.Spec.ReplicasPerMaster are defined
	if rc.Spec.Replicas < 3 || replicas < 3 {
		return false, fmt.Errorf("replicas must be >= 3")
	}
	if rc.Spec.ReplicasPerMaster < 1 || replicasPerMaster < 1 {
		return false, fmt.Errorf("replicasPerMaster must be >= 1")
	}

	// Expected total pods in the StatefulSet:
	expectedSTS := rc.Spec.Replicas + rc.Spec.Replicas*rc.Spec.ReplicasPerMaster

	// Fetch the StatefulSet
	sts := &appsv1.StatefulSet{}
	if err := c.Get(ctx, key, sts); err != nil {
		return false, err
	}

	actualSTS := *sts.Spec.Replicas
	if expectedSTS != actualSTS {
		return false, fmt.Errorf(
			"statefulset replicas %d != expected %d", actualSTS, expectedSTS,
		)
	}

	// Finally ensure the Spec values match the inputs
	if rc.Spec.Replicas != replicas || rc.Spec.ReplicasPerMaster != replicasPerMaster {
		return false, fmt.Errorf(
			"spec (%d,%d) != expected (%d,%d)",
			rc.Spec.Replicas, rc.Spec.ReplicasPerMaster,
			replicas, replicasPerMaster,
		)
	}

	return true, nil
}

func InsertDataIntoCluster(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName, redisCluster *redisv1.RedisCluster) (bool, error) {
	selectedPods := &corev1.PodList{}
	// Wait for ready status of redis-cluster
	_, err := WaitForReady(ctx, k8sClient, nsName)
	if err != nil {
		return false, err
	}
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	err = k8sClient.List(ctx, selectedPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return false, err
	}

	err = createAndInsertDataIntoCluster(selectedPods)
	if err != nil {
		return false, err
	}

	isOk, err := CheckClusterKeys(selectedPods)
	if err != nil {
		return false, err
	}

	return isOk, nil
}

func RemoveServicePorts(ctx context.Context, c client.Client, key types.NamespacedName) error {
	return updateService(ctx, c, key, func(svc *corev1.Service) {
		svc.Spec.Ports = nil
	})
}

func AddServicePorts(ctx context.Context, c client.Client, key types.NamespacedName) error {
	return updateService(ctx, c, key, func(svc *corev1.Service) {
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "gossip",
				Port:       redis.RedisGossPort,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(int(redis.RedisGossPort)),
			},
			{
				Name:       "comm",
				Port:       redis.RedisCommPort,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(int(redis.RedisCommPort)),
			},
		}
	})
}

func updateService(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	mutate func(*corev1.Service),
) error {

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		svc := &corev1.Service{}
		if err := c.Get(ctx, key, svc); err != nil {
			// If the Service is gone or any other error occurs, abort retries.
			return err
		}

		// apply the caller-supplied mutation
		mutate(svc)

		return c.Update(ctx, svc)
	})
}

func ForgetANode(k8Client client.Client, ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	allPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	k8Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})

	err := forgetFirstNode(allPods)

	if err != nil {
		return err
	}

	return nil
}

func ForgetANodeFixAndMeet(k8Client client.Client, ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	allPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	k8Client.List(ctx, allPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})

	err := forgetFirstNode(allPods)
	if err != nil {
		return err
	}

	err = fixCluster(allPods.Items[1])
	if err != nil {
		return err
	}

	err = meetNode(allPods.Items[0], allPods.Items[1])
	if err != nil {
		return err
	}

	err = meetNode(allPods.Items[0], allPods.Items[2])
	if err != nil {
		return err
	}

	return nil
}

// Choose the first node represented by the first pod in the list and executes the
// forget command on the other nodes.
func forgetFirstNode(pods *corev1.PodList) error {
	var cmd string
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found")
	}
	if len(pods.Items) < 2 {
		return fmt.Errorf("only one node, no nodes to forget")
	}

	pod := pods.Items[0]

	cmd = "redis-cli cluster nodes | grep myself | awk '{ print $1 }'"
	podID, _, err := remoteCommand(pod.Namespace, pod.Name, cmd)
	if err != nil {
		return err
	}
	for i := 1; i < len(pods.Items); i++ {
		pod = pods.Items[i]
		cmd = fmt.Sprintf("%s %s", "redis-cli cluster forget", podID)
		_, _, err := remoteCommand(pod.Namespace, pod.Name, cmd)
		if err != nil {
			return err
		}
	}

	return nil
}

// Launches the command redis-cli --cluster fix to fix the cluster auto responding yes
// if prompted to reshard the slots not covered.
func fixCluster(pod corev1.Pod) error {
	cmd := "echo \"yes\" | redis-cli --cluster fix localhost:6379"
	_, _, err := remoteCommand(pod.Namespace, pod.Name, cmd)
	if err != nil {
		return err
	}

	return nil
}

// Meets pod1 into the cluster launching the command cluster meet on node pod2.
func meetNode(pod1 corev1.Pod, pod2 corev1.Pod) error {
	cmd := "redis-cli cluster nodes | grep myself | awk '{ print $2 }' | awk -F ':' '{ print $1 }'"
	IPPod1, _, err := remoteCommand(pod1.Namespace, pod1.Name, cmd)
	if err != nil {
		return err
	}
	cmd = fmt.Sprintf("redis-cli cluster meet %s 6379", strings.TrimSuffix(IPPod1, "\n"))
	_, _, err = remoteCommand(pod2.Namespace, pod2.Name, cmd)
	if err != nil {
		return err
	}

	return nil
}
