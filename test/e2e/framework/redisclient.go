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
	DefaultRedisImage             = "axinregistry1.central.inditex.grp/itxapps/redis.redis-stack-server:7.4.0-v3-coordinator"
	defaultConfig                 = `save ""
appendonly no
maxmemory 70mb`
	finalizerName  = "redis.inditex.com/configmap-cleanup"
	pollInterval   = 10 * time.Second
	defaultTimeout = 10 * time.Minute
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
	storage string,
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
			Image:                DefaultRedisImage,
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

// WaitForStatus blocks until the RedisCluster's .Status.Status matches desiredStatus,
// or until timeout/poll elapses (or ctx is canceled).  Returns the last fetched object.
func WaitForStatus(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	desiredStatus string,
) (*redisv1.RedisCluster, error) {
	var last redisv1.RedisCluster
	err := wait.PollUntilContextTimeout(ctx, pollInterval, defaultTimeout, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, key, &last); err != nil {
			if errors.IsNotFound(err) {
				return false, nil // keep polling until it's created
			}
			return false, err
		}
		return last.Status.Status == desiredStatus, nil
	})
	if err != nil {
		return nil, fmt.Errorf(
			"timed out waiting for %s/%s status=%q (last: %q): %w",
			key.Namespace, key.Name, desiredStatus, last.Status.Status, err,
		)
	}
	return &last, nil
}

// WaitForReady is a convenience wrapper to wait for “Ready”
func WaitForReady(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
) (*redisv1.RedisCluster, error) {
	return WaitForStatus(ctx, c, key, "Ready")
}

// ChangeClusterOptions holds all the parameters for a cluster update
type ChangeClusterOptions struct {
	// Precondition: we wait until this status before mutating
	CurrentStatus string
	// Sequence of statuses to wait *after* mutation, in order
	NextStatuses []string
	// The actual mutation of the RedisCluster.Spec
	Mutate func(rc *redisv1.RedisCluster)
}

// ChangeCluster does the full cycle: wait current, mutate, then wait through
// each of opts.NextStatuses in turn, but will skip any intermediate status
// that was bypassed in favor of a later one.
func ChangeCluster(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	opts ChangeClusterOptions,
) (*redisv1.RedisCluster, error) {
	// 1) Wait initial status
	if _, err := WaitForStatus(ctx, c, key, opts.CurrentStatus); err != nil {
		return nil, err
	}

	// 2) Upsert via CreateOrUpdate, wrapped in RetryOnConflict
	rc := &redisv1.RedisCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, upErr := controllerutil.CreateOrUpdate(ctx, c, rc, func() error {
			opts.Mutate(rc)
			controllerutil.AddFinalizer(rc, finalizerName)
			return nil
		})
		return upErr
	}); err != nil {
		return nil, fmt.Errorf("upsert RedisCluster %s/%s: %w", key.Namespace, key.Name, err)
	}

	// 3) Wait through each post‑mutation status, skipping if already at final
	var last *redisv1.RedisCluster
	final := opts.NextStatuses[len(opts.NextStatuses)-1]
	for _, st := range opts.NextStatuses {
		next, wErr := WaitForStatus(ctx, c, key, st)
		if wErr != nil {
			// maybe we already jumped straight to `final`?
			curr := &redisv1.RedisCluster{}
			if getErr := c.Get(ctx, key, curr); getErr == nil && curr.Status.Status == final {
				last = curr
				continue
			}
			return nil, wErr
		}
		last = next
	}
	return last, nil
}

// Helper wrappers for common operations:

// ChangeConfiguration scales/ephemeral/image/etc, waiting through ScalingUp → Ready
func ChangeConfiguration(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	replicas, replicasPerMaster int32,
	ephemeral bool,
	resources *corev1.ResourceRequirements,
	image string,
	pdb redisv1.Pdb,
	override redisv1.RedisClusterOverrideSpec,
	expectedStatus []string,
) (*redisv1.RedisCluster, error) {
	return ChangeCluster(ctx, c, key, ChangeClusterOptions{
		CurrentStatus: "Ready",
		NextStatuses:  expectedStatus,
		Mutate: func(rc *redisv1.RedisCluster) {
			rc.Spec.Replicas = replicas
			rc.Spec.Ephemeral = ephemeral
			if replicasPerMaster > 0 {
				rc.Spec.ReplicasPerMaster = replicasPerMaster
			}
			if resources != nil {
				rc.Spec.Resources = resources
			}
			if image != "" {
				rc.Spec.Image = image
			}
			rc.Spec.Pdb = pdb
			rc.Spec.Override = &override
		},
	})
}

// ChangeStorage toggles PVC/on/off, waiting through Error → Ready
func ChangeStorage(
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	storage string,
	replicas int32,
) (*redisv1.RedisCluster, error) {
	return ChangeCluster(ctx, c, key, ChangeClusterOptions{
		CurrentStatus: "Ready",
		NextStatuses:  []string{"Error", "Ready"},
		Mutate: func(rc *redisv1.RedisCluster) {
			rc.Spec.Storage = storage
			rc.Spec.DeletePVC = (storage != "")
			rc.Spec.Ephemeral = (storage == "")
			rc.Spec.Replicas = replicas
		},
	})
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

func waitForObjectStatus(ctx context.Context, client client.Client, nsName types.NamespacedName, desiredStatus string, timeoutSeconds int32) error {
	interval := 3 * time.Second
	duration := time.Duration(timeoutSeconds) * time.Second
	var lastKnownStatus string

	err := wait.PollUntilContextTimeout(ctx, interval, duration, true, func(ctx context.Context) (bool, error) {
		redisCluster := &redisv1.RedisCluster{}
		err := client.Get(ctx, nsName, redisCluster)
		if err != nil {
			if !errors.IsNotFound(err) { // Continue polling if object is not found
				return false, nil
			}
			return false, err
		}

		lastKnownStatus = redisCluster.Status.Status
		return lastKnownStatus == desiredStatus, nil
	})

	if err != nil {
		return fmt.Errorf("timeout after %s waiting for status '%s', last known status: '%s': %w", duration, desiredStatus, lastKnownStatus, err)
	}
	return nil
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
	err := waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
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

func RemoveServicePorts(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName) error {
	var ports []corev1.ServicePort
	fetchedService := &corev1.Service{}
	err := k8sClient.Get(ctx, nsName, fetchedService)
	if err != nil {
		fmt.Printf("Error getting service %s", nsName)
		return err
	}
	fetchedService.Spec.Ports = ports
	err = k8sClient.Update(ctx, fetchedService)
	if err != nil {
		fmt.Printf("Error updateing service %s", nsName)
		return err
	}
	return nil
}

func AddServicePorts(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName) error {
	var ports []corev1.ServicePort
	fetchedService := &corev1.Service{}
	err := k8sClient.Get(ctx, nsName, fetchedService)
	if err != nil {
		fmt.Printf("Error getting service %s", nsName)
		return err
	}
	targetPort := intstr.IntOrString{IntVal: redis.RedisGossPort}
	port := corev1.ServicePort{Name: "gossip", Port: redis.RedisGossPort, Protocol: "TCP", TargetPort: targetPort}
	ports = append(ports, port)
	targetPort = intstr.IntOrString{IntVal: redis.RedisCommPort}
	port = corev1.ServicePort{Name: "comm", Port: redis.RedisCommPort, Protocol: "TCP", TargetPort: targetPort}
	ports = append(ports, port)
	fetchedService.Spec.Ports = ports
	err = k8sClient.Update(ctx, fetchedService)
	if err != nil {
		fmt.Printf("Error updateing service %s", nsName)
		return err
	}
	return nil
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
