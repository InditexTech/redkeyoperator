package framework

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"math/rand"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/redis"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	DBSIZECMD              string = "redis-cli DBSIZE"
	FLUSHCMD               string = "redis-cli FLUSHALL"
	CLUSTERINFOCMD         string = "redis-cli cluster info"
	CLUSTERNODESCMD        string = "redis-cli cluster nodes"
	EXPECTEDKEYS           int    = 30
	WAIT_FOR_RDCL_DELETION int    = 180
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

var (
	version = "6.0.2"
)

func EnsureClusterExistsOrCreate(client client.Client, nsName types.NamespacedName, replicas int32, storage string, replicasPerMaster int32, purgeKeys bool, ephemeral bool, pdb redisv1.Pdb, override redisv1.RedisClusterOverrideSpec) error {
	rc := &redisv1.RedisCluster{}
	err := client.Get(context.TODO(), nsName, rc)
	if rc.Status.Status != "Ready" && err == nil {
		if err := client.Delete(context.TODO(), rc); err != nil {
			return err
		}
		newCluster := createRedisCluster(nsName, replicas, storage, replicasPerMaster, purgeKeys, ephemeral, pdb, override)
		if err := client.Create(context.TODO(), newCluster); err != nil {
			return err
		}
	} else if err != nil {
		newCluster := createRedisCluster(nsName, replicas, storage, replicasPerMaster, purgeKeys, ephemeral, pdb, override)
		if err := client.Create(context.TODO(), newCluster); err != nil {
			return err
		}
	}
	return nil
}

func ChangeRedisClusterConfiguration(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName, labels map[string]string, status string, replicas int32, replicasPerMaster int32, ephemeral bool, resources *corev1.ResourceRequirements, expectedImage string, pdb redisv1.Pdb, override redisv1.RedisClusterOverrideSpec) (*redisv1.RedisCluster, error) {
	// Wait for ready status of redis-cluster
	fetched := &redisv1.RedisCluster{}
	err := k8sClient.Get(ctx, nsName, fetched)
	if err != nil {
		return nil, err
	}
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(500))
	if err != nil {
		return nil, err
	}

	// Update labels of redis-cluster
	err = updateRedisCluster(ctx, k8sClient, fetched, "", replicas, labels, replicasPerMaster, ephemeral, resources, expectedImage, pdb, override)
	if err != nil {
		return nil, err
	}

	err = waitForObjectStatus(ctx, k8sClient, nsName, status, int32(800))
	if err != nil {
		return nil, err
	}

	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return nil, err
	}

	return fetched, nil
}

func ChangeRedisClusterStorage(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName, initialStorage string, desiredStorage string, replicas int32) (*redisv1.RedisCluster, error) {
	fetched := &redisv1.RedisCluster{}
	err := k8sClient.Get(ctx, nsName, fetched)
	if err != nil {
		return nil, err
	}
	// Wait for ready status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return nil, err
	}
	// Update the storage with the desired value
	err = updateRedisCluster(ctx, k8sClient, fetched, desiredStorage, replicas, nil, 0, false, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
	if err != nil {
		return nil, err
	}
	// Wait for scaling-up status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Error", int32(800))
	if err != nil {
		return nil, err
	}
	// Update the storage with initial value of redis-cluster
	err = updateRedisCluster(ctx, k8sClient, fetched, initialStorage, replicas, nil, 0, false, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
	if err != nil {
		return nil, err
	}
	// Wait for ready status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return nil, err
	}

	return fetched, nil
}
func WaitForReadyRdcl(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName) (*redisv1.RedisCluster, error) {
	fetched := &redisv1.RedisCluster{}
	err := k8sClient.Get(ctx, nsName, fetched)
	if err != nil {
		return nil, err
	}
	// Wait for ready status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return nil, err
	}

	return fetched, nil
}

func ChangeRedisClusterEphemeral(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName, storage string, replicas int32) (*redisv1.RedisCluster, error) {
	fetched := &redisv1.RedisCluster{}
	err := k8sClient.Get(ctx, nsName, fetched)
	if err != nil {
		return nil, err
	}
	// Wait for ready status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return nil, err
	}
	// Update the storage with the desired value
	err = updateRedisCluster(ctx, k8sClient, fetched, storage, replicas, nil, 0, false, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
	if err != nil {
		return nil, err
	}
	// Wait for Error status of redis-cluster
	err = waitForObjectStatus(ctx, k8sClient, nsName, "Error", int32(800))
	if err != nil {
		return nil, err
	}

	return fetched, nil
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

func CheckRedisClusterIfError(k8Client client.Client, ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
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

	return rdclStatus.State == "Error", nil
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

func createRedisCluster(nsName types.NamespacedName, replicas int32, storage string, replicasPerMaster int32, purgeKeys bool, ephemeral bool, pdb redisv1.Pdb, override redisv1.RedisClusterOverrideSpec) *redisv1.RedisCluster {
	cluster := &redisv1.RedisCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RedisCluster",
			APIVersion: "redis.inditex.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       nsName.Name,
			Namespace:  nsName.Namespace,
			Finalizers: []string{"redis.inditex.com/configmap-cleanup"},
			Labels:     map[string]string{"team": "team-a"},
		},
		Spec: redisv1.RedisClusterSpec{
			Auth:      redisv1.RedisAuth{},
			Pdb:       pdb,
			Version:   version,
			Replicas:  replicas,
			Ephemeral: true,
			Image:     "redislabs/redisgraph:2.8.9",
			Config: `
			maxmemory 70mb
			maxmemory-samples 5
			maxmemory-policy allkeys-lru
			appendonly no
			protected-mode no
			rdbcompression no
			rdbchecksum no
	`,
			Resources: &corev1.ResourceRequirements{
				Limits:   newLimits(),
				Requests: newRequests(),
			},
			PurgeKeysOnRebalance: purgeKeys,
			Override:             &override,
		},
	}

	if storage != "" {
		cluster.Spec.DeletePVC = true
		cluster.Spec.Ephemeral = false
		cluster.Spec.Storage = storage
		accessModesTypes := make([]corev1.PersistentVolumeAccessMode, 0, 3)
		accessModesTypes = append(accessModesTypes, corev1.ReadWriteOnce)
		cluster.Spec.AccessModes = accessModesTypes
	}

	if replicasPerMaster > 0 {
		cluster.Spec.ReplicasPerMaster = replicasPerMaster
	}

	cluster.Spec.Ephemeral = ephemeral

	return cluster
}

func updateRedisCluster(context context.Context, client client.Client, redisCluster *redisv1.RedisCluster, storage string, replicas int32, expectedLabels map[string]string, expectedReplicasPerMaster int32, ephemeral bool, resources *corev1.ResourceRequirements, image string, pdb redisv1.Pdb, override redisv1.RedisClusterOverrideSpec) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		copyRedisCluster := &redisv1.RedisCluster{}
		err := client.Get(context, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, copyRedisCluster)
		if err != nil {
			return err
		}

		copyRedisCluster.Spec.Ephemeral = ephemeral
		copyRedisCluster.Spec.Replicas = replicas

		if expectedReplicasPerMaster > 0 {
			copyRedisCluster.Spec.ReplicasPerMaster = expectedReplicasPerMaster
		}

		if storage != "" {
			copyRedisCluster.Spec.Storage = storage
		}
		if image != "" {
			copyRedisCluster.Spec.Image = image
		}

		if expectedLabels != nil {
			copyRedisCluster.Spec.Labels = &expectedLabels
		}

		if resources != nil {
			copyRedisCluster.Spec.Resources = resources
		}

		copyRedisCluster.Spec.Pdb = pdb
		copyRedisCluster.Spec.Override = &override

		// Update the object
		err = client.Update(context, copyRedisCluster)
		if err != nil {
			return err
		}

		return nil
	})

	return err
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
	return wait.PollImmediateWithContext(ctx, 1*time.Second, 300*time.Second, func(ctx context.Context) (bool, error) {
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

	err := wait.PollImmediateWithContext(ctx, interval, duration, func(ctx context.Context) (bool, error) {
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

func ValidateRedisClusterMasterSlave(ctx context.Context, k8Client client.Client, nsName types.NamespacedName, replicas int32, replicasPerMaster int32, redisCluster *redisv1.RedisCluster) (bool, error) {
	isValid := true
	stsReplicas := int32(0)
	rdclReplicas := int32(0)
	rdclReplicasPerMaster := int32(0)
	minRdclRepSlaves := int32(0)
	minRepStatefulSet := int32(0)
	fetchedStatefulset := &appsv1.StatefulSet{}
	// Wait for ready status of redis-cluster
	err := waitForObjectStatus(ctx, k8Client, nsName, "Ready", int32(60))
	if err != nil {
		return false, err
	}
	rdclReplicas = redisCluster.Spec.Replicas
	rdclReplicasPerMaster = redisCluster.Spec.ReplicasPerMaster

	// validate initial replicas values
	if rdclReplicas < 3 || replicas < 3 {
		return false, fmt.Errorf("redis-cluster replicas must be greater than 3")
	}
	if rdclReplicasPerMaster < 1 || replicasPerMaster < 1 {
		return false, fmt.Errorf("redis-cluster replicasPerMaster must be greater than 3")
	}

	// Calculate the number of replicas for the statefulset
	minRdclRepSlaves = rdclReplicas * rdclReplicasPerMaster
	minRepStatefulSet = rdclReplicas + minRdclRepSlaves
	// get statefulset associated to redis-cluster
	err = k8Client.Get(ctx, nsName, fetchedStatefulset)
	if err != nil {
		return false, err
	}

	// get the actual number of replicas from statefulset
	stsReplicas = *fetchedStatefulset.Spec.Replicas
	if rdclReplicas != replicas {
		return false, fmt.Errorf("redis-cluster replicas must be equal to replicas")
	}
	if rdclReplicasPerMaster != replicasPerMaster {
		return false, fmt.Errorf("redis-cluster replicasPerMaster must be equal to replicasPerMaster")
	}
	if minRepStatefulSet != stsReplicas {
		return false, fmt.Errorf("statefulset replicas must be equal to calculated number of replicas with replicas and replicasPerMaster")
	}
	return isValid, nil
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

func InsertDataAndScaleIntoCluster(ctx context.Context, k8sClient client.Client, nsName types.NamespacedName, redisCluster *redisv1.RedisCluster, status string, replicas int32) (bool, error) {
	fetched := &redisv1.RedisCluster{}
	err := k8sClient.Get(ctx, nsName, fetched)
	if err != nil {
		return false, err
	}

	selectedPods := &corev1.PodList{}

	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"redis-cluster-name":                    redisCluster.Name,
			"redis.rediscluster.operator/component": "redis",
		},
	)

	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		return false, err
	}

	err = k8sClient.List(ctx, selectedPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return false, err
	}

	err = createAndInsertDataIntoCluster(selectedPods)
	if err != nil {
		fmt.Printf("%v,error in create data", err)
		return false, err
	}

	// Update labels of redis-cluster
	err = updateRedisCluster(ctx, k8sClient, fetched, "", replicas, nil, 0, true, nil, "", redisv1.Pdb{}, redisv1.RedisClusterOverrideSpec{})
	if err != nil {
		fmt.Printf("%v,error in update", err)
		return false, err
	}

	err = waitForObjectStatus(ctx, k8sClient, nsName, status, int32(800))
	if err != nil {
		fmt.Printf("%v,error in status scale", err)
		return false, err
	}

	err = waitForObjectStatus(ctx, k8sClient, nsName, "Ready", int32(800))
	if err != nil {
		fmt.Printf("%v,error in status ready", err)
		return false, err
	}

	err = k8sClient.List(ctx, selectedPods, &client.ListOptions{
		Namespace:     redisCluster.Namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return false, err
	}

	isOk, err := CheckClusterKeys(selectedPods)
	if err != nil {
		fmt.Printf("%v,error in create data", err)
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

func EnsureClusterIsDeleted(client client.Client, nsName types.NamespacedName, fetchedRedisCluster *redisv1.RedisCluster) error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	err := client.Delete(ctx, fetchedRedisCluster)
	if err != nil {
		return err
	}
	err = waitForRedisClusterDeletion(ctx, client, nsName, int32(WAIT_FOR_RDCL_DELETION))
	if err != nil {
		return err
	}
	return nil
}

func waitForRedisClusterDeletion(ctx context.Context, client client.Client, nsName types.NamespacedName, timeoutSeconds int32) error {
	interval := 3 * time.Second
	duration := time.Duration(timeoutSeconds) * time.Second

	err := wait.PollImmediateWithContext(ctx, interval, duration, func(ctx context.Context) (bool, error) {
		redisCluster := &redisv1.RedisCluster{}
		err := client.Get(ctx, nsName, redisCluster)
		if err != nil {
			if !errors.IsNotFound(err) { // Continue polling if object is not yet deleted
				return false, err
			}
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		return fmt.Errorf("timeout after %s waiting for RedisCluster being deleted: %w", duration, err)
	}
	return nil
}
