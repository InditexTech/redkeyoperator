// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/md5"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	finalizer "github.com/inditextech/redisoperator/internal/finalizers"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	redis "github.com/inditextech/redisoperator/internal/redis"
	"github.com/inditextech/redisoperator/internal/utils"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MIGRATE_TIMEOUT                       = 6000
	DEFAULT_REQUEUE_TIMEOUT time.Duration = 5
	READY_REQUEUE_TIMEOUT   time.Duration = 30
	ERROR_REQUEUE_TIMEOUT   time.Duration = 30
)

func NewRedisClusterReconciler(mgr ctrl.Manager, maxConcurrentReconciles int, concurrentMigrates int) *RedisClusterReconciler {
	eventRecorder := mgr.GetEventRecorderFor("rediscluster-controller")
	reconciler := &RedisClusterReconciler{
		Client:                  mgr.GetClient(),
		Log:                     ctrl.Log.WithName("controllers").WithName("RedisCluster"),
		Scheme:                  mgr.GetScheme(),
		Recorder:                eventRecorder,
		MaxConcurrentReconciles: maxConcurrentReconciles,
		ConcurrentMigrate:       concurrentMigrates,
		Finalizers: []finalizer.Finalizer{
			&finalizer.BackupFinalizer{},
			&finalizer.ConfigMapCleanupFinalizer{},
			&finalizer.DeletePVCFinalizer{},
		},
	}

	reconciler.GetClusterInfoFunc = reconciler.DoGetClusterInfo
	reconciler.GetReadyNodesFunc = reconciler.DoGetReadyNodes
	reconciler.FindExistingStatefulSetFunc = reconciler.DoFindExistingStatefulSet
	reconciler.FindExistingConfigMapFunc = reconciler.DoFindExistingConfigMap
	reconciler.FindExistingDeploymentFunc = reconciler.DoFindExistingDeployment
	reconciler.FindExistingPodDisruptionBudgetFunc = reconciler.DoFindExistingPodDisruptionBudget

	return reconciler
}

// Generates a slice containing the list of labels that do not have to be populated
// from RedisCluster.Metadata.Labels to RedisCluster.Spec.Labels.
func getDiscardedLabels() []string {
	return []string{"scaler/opt-in", "scaler/scaling-class"}
}

func (r *RedisClusterReconciler) deletePdb(ctx context.Context, redisCluster *redisv1.RedisCluster) {
	// Delete PodDisruptionBudget
	pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogInfo(redisCluster.NamespacedName(), "PodDisruptionBudget not deployed", "PodDisruptionBudget Name", redisCluster.Name+"-pdb")
	} else {
		err = r.DeletePodDisruptionBudget(ctx, pdb, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "PodDisruptionBudget Deleted")
		}
	}
}

func (r *RedisClusterReconciler) ReconcileClusterObject(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) (ctrl.Result, error) {
	currentStatus := redisCluster.Status
	var err error
	const pvcFinalizer = "redis.inditex.dev/delete-pvc"

	var requeueAfter time.Duration = DEFAULT_REQUEUE_TIMEOUT
	var auth = &corev1.Secret{}

	r.LogInfo(redisCluster.NamespacedName(), "RedisCluster reconciler start", "status", redisCluster.Status.Status)

	if len(redisCluster.Spec.Auth.SecretName) > 0 {
		var err error
		auth, err = r.GetSecret(ctx, types.NamespacedName{
			Name:      redisCluster.Spec.Auth.SecretName,
			Namespace: req.Namespace,
		}, redisCluster.NamespacedName())
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Can't find provided secret", "redisCluster", redisCluster)
			return ctrl.Result{}, nil
		}
	}

	// Populate Spec.Labels if not present (if RedisCluster created from template <= v2.5.0)
	if redisCluster.Spec.Labels == nil {
		discardedLabels := getDiscardedLabels()
		specLabels := make(map[string]string)
		for k, v := range redisCluster.ObjectMeta.Labels {
			if !slices.Contains(discardedLabels, k) {
				specLabels[k] = v
			}
		}
		redisCluster.Spec.Labels = &specLabels
		if err := r.Update(ctx, redisCluster); err != nil {
			return ctrl.Result{}, err
		}
		r.LogInfo(redisCluster.NamespacedName(), "RedisCluster.Spec.Labels populated from RedisCluster.Metadata.Labels")
	}

	// Checks the existance of the ConfigMap, StatefulSet, Pods, Robin Deployment, PDB and Service,
	// creating the objects not created yet.
	// Coherence and configuration details are also checked and fixed.
	immediateRequeue, err := r.CheckAndCreateK8sObjects(ctx, req, redisCluster, auth)
	if err != nil {
		return ctrl.Result{}, err
	}
	if immediateRequeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, err
	}

	// Check storage configuration consistency, updating status if needed.
	r.checkStorageConfigConsistency(ctx, redisCluster, redisCluster.Status.Status != redisv1.StatusError)

	// Redis cluster scaled to 0 replicas?
	// If it's a newly deployed cluster it won't have a status set yet and won't be catched it here.
	if redisCluster.Spec.Replicas == 0 && redisCluster.Status.Status != "" {
		err = r.clusterScaledToZeroReplicas(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error managing cluster scaled to 0 replicas")
		}

		// Requeue to recheck, reconciliation ends here!
		r.LogInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)
		return ctrl.Result{RequeueAfter: time.Second * READY_REQUEUE_TIMEOUT}, err
	}

	if redisCluster.Spec.DeletePVC && !redisCluster.Spec.Ephemeral {
		r.LogInfo(redisCluster.NamespacedName(), "Delete PVCs feature enabled in cluster spec")
		if !controllerutil.ContainsFinalizer(redisCluster, pvcFinalizer) {
			controllerutil.AddFinalizer(redisCluster, pvcFinalizer)
			r.Update(ctx, redisCluster)
			r.LogInfo(redisCluster.NamespacedName(), "Added finalizer. Deleting PVCs after scale down or cluster deletion")
		}
	} else {
		r.LogInfo(redisCluster.NamespacedName(), "Delete PVCs feature disabled in cluster spec or not specified. Not deleting PVCs after scale down or cluster deletion")
	}

	// Kubernetes API
	if !redisCluster.GetDeletionTimestamp().IsZero() {
		for _, f := range r.Finalizers {
			if containsString(redisCluster.GetFinalizers(), f.GetId()) {
				r.LogInfo(redisCluster.NamespacedName(), "Running finalizer", "id", f.GetId(), "finalizer", f)
				finalizerError := f.DeleteMethod(ctx, redisCluster, r.Client)
				if finalizerError != nil {
					r.LogError(redisCluster.NamespacedName(), finalizerError, "Finalizer returned error", "id", f.GetId(), "finalizer", f)
				}
				controllerutil.RemoveFinalizer(redisCluster, f.GetId())
				if err := r.Update(ctx, redisCluster); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	// Redis API

	r.checkClusterNodesIntegrity(ctx, redisCluster)

	readyNodes, err := r.GetReadyNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error getting redis cluster nodes")
		return ctrl.Result{}, err
	}
	redisCluster.Status.Nodes = readyNodes

	requeue := false
	switch redisCluster.Status.Status {
	case redisv1.StatusConfiguring:
		requeue, requeueAfter = r.reconcileStatusConfiguring(ctx, redisCluster)
	case redisv1.StatusReady:
		requeue, requeueAfter = r.reconcileStatusReady(ctx, redisCluster)
	case redisv1.StatusUpgrading:
		requeue, requeueAfter = r.reconcileStatusUpgrading(ctx, redisCluster)
	case redisv1.StatusScalingDown:
		requeue, requeueAfter = r.reconcileStatusScalingDown(ctx, redisCluster)
	case redisv1.StatusScalingUp:
		requeue, requeueAfter = r.reconcileStatusScalingUp(ctx, redisCluster)
	case redisv1.StatusError:
		requeue, requeueAfter = r.reconcileStatusError(ctx, redisCluster)
	default:
		r.CheckConfigurationStatus(ctx, redisCluster)
	}

	r.LogInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)

	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, currentStatus) {
		update_err = r.UpdateClusterStatus(ctx, redisCluster)
	}

	if requeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, update_err
	}
	return ctrl.Result{}, update_err
}

func (r *RedisClusterReconciler) checkClusterNodesIntegrity(ctx context.Context, redisCluster *redisv1.RedisCluster) {

	if redisCluster.Status.Status == redisv1.StatusInitializing {
		return
	}

	// At a minimum, we have to have the pods ready. You can reach this point when you are creating
	// a new cluster, in Configuring status.
	podsReady, err := r.AllPodsReady(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check ready pods")
	}
	if !podsReady {
		return
	}

	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	logger := r.GetHelperLogger(redisCluster.NamespacedName())

	// Forget outdated nodes.
	err = clusterNodes.RemoveClusterOutdatedNodes(ctx, redisCluster, logger)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check outdated nodes")
	}

	// Meet nodes if needed.
	needsMeet, err := clusterNodes.NeedsClusterMeet(ctx)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check if cluster meet is necessary")
		return
	}
	if needsMeet {
		err = clusterNodes.ClusterMeet(ctx)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not cluster meet")
			return
		}
	}

	// Ensure cluster master-replica nodes ratio.
	err = clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not assign missing slots")
	}

	// Fix open slots (only master nodes).
	openSlots := false
	for _, node := range clusterNodes.Nodes {
		if !node.IsMaster() {
			continue
		}
		openSlots, err = node.HasOpenSlots()
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not check if node has open slots")
			break
		}
		if openSlots {
			r.LogInfo(redisCluster.NamespacedName(), "Open slots found. Cleanup needed")
			err = clusterNodes.EnsureClusterSlotsStable(ctx, logger)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Could not check if slots are stable")
			}
			break
		}
	}
}

// Checks storage configuration for inconsistencies.
// If the parameter is set to true if a check fail the function returns issuing log info, generating an event and setting the Redis cluster Status to Error.
// Returns true if all checks pass or false if any checks fail.
func (r *RedisClusterReconciler) checkStorageConfigConsistency(ctx context.Context, redisCluster *redisv1.RedisCluster, updateRDCL bool) bool {
	var stsStorage, stsStorageClassName string

	sts, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}
	if sts == nil {
		err = errors.NewNotFound(schema.GroupResource{Group: "", Resource: "Statefulset"}, "StatefulSet not found")
		r.LogError(redisCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}

	// Get configured Storage and StorageClassName from StatefulSet
	if len(sts.Spec.VolumeClaimTemplates) > 0 {
		stsStorage = sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()
		r.LogInfo(redisCluster.NamespacedName(), "Current StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage", stsStorage)
		if sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName != nil {
			stsStorageClassName = *sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName
			r.LogInfo(redisCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", stsStorageClassName)
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", "Not set")
		}
	}

	// Non ephemeral cluster checks:
	// - Updates to redisCluster.Spec.Storage are not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.Storage != "" && stsStorage != "" && redisCluster.Spec.Storage != stsStorage {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.Storage field are not allowed")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redisCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Unset redisCluster.Spec.Storage is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.Storage == "" && stsStorage != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.Storage field are not allowed")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redisCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Updates to redisCluster.Spec.StorageClassName are not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName != "" && stsStorageClassName != "" && redisCluster.Spec.StorageClassName != stsStorageClassName {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Set redisCluster.Spec.StorageClassName is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName != "" && stsStorageClassName == "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Unset redisClusterSpec.StorageClassName is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName == "" && stsStorageClassName != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS stsStorageClassName", stsStorageClassName, "RDCL stsStorageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Moving from ephemeral to non ephemeral is not allowed
	if !redisCluster.Spec.Ephemeral && len(sts.Spec.VolumeClaimTemplates) == 0 {
		err = errors.NewBadRequest("spec: Error: non ephemeral cluster without VolumeClaimTemplates defined in the StatefulSet")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from ephemeral to non ephemeral)")
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// Ephemeral cluster checks:
	// - Moving from non ephemeral to ephemeral is not allowed
	if redisCluster.Spec.Ephemeral && len(sts.Spec.VolumeClaimTemplates) > 0 {
		err = errors.NewBadRequest("spec: Error: ephemeral cluster with VolumeClaimTemplates defined in the StatefulSet")
		r.LogError(redisCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from non ephemeral to ephemeral)")
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}

	return true
}

func (r *RedisClusterReconciler) reconcileStatusConfiguring(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true

	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	err = r.ConfigureRedisCluster(ctx, redisCluster, clusterNodes)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when configuring cluster. Will retry again.")
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}

	// Cluster meet if needed.
	err = r.CheckAndMeet(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when checking if the cluster needs a meet. Will retry again.")
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}

	// Rebalance if needed.
	err = r.CheckAndBalance(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when checking if the cluster is balanced. Will retry again.")
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}

	// PodDisruptionBudget update
	if redisCluster.Spec.Pdb.Enabled && redisCluster.Spec.Replicas > 1 {
		err = r.UpdatePodDisruptionBudget(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "ScaleCluster - Failed to update PodDisruptionBudget")
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "ScaleCluster - PodDisruptionBudget updated ", "Name", redisCluster.Name+"-pdb")
		}
	}

	r.CheckConfigurationStatus(ctx, redisCluster)

	return requeue, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusReady(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter time.Duration = READY_REQUEUE_TIMEOUT
	podsReady, err := r.AllPodsReady(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check ready pods")
	}
	if podsReady {
		clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
			return requeue, requeueAfter
		}
		// Free cluster nodes to avoid memory consumption
		defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

		requeueAfter = READY_REQUEUE_TIMEOUT
		logger := r.GetHelperLogger(redisCluster.NamespacedName())
		err = clusterNodes.EnsureClusterRatio(ctx, redisCluster, logger)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not assign missing slots")
		}
		// We want to check if any slots are missing
		err = r.AssignMissingSlots(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not assign missing slots")
		}
	}

	// Check and update RedisCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.UpdateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.UpdateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
		}
	}
	// -> The cluster needs a meet or rebalance: switch to Configuring status.
	err = r.UpdateConfiguringStatusMeetOrBalance(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when updating configuration status if a meet or rebalance is required")
	}

	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.CheckPDB(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error checking PDB changes")
		}
	}

	// Requeue to check periodically the cluster is well formed and fix it if needed
	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) reconcileStatusUpgrading(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true

	err := r.UpgradeCluster(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when upgrading cluster")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}

	return requeue, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusScalingDown(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	err := r.ScaleCluster(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when scaling down")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}
	err = r.UpdateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusScalingUp(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	err := r.ScaleCluster(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when scaling up")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DEFAULT_REQUEUE_TIMEOUT
	}
	err = r.UpdateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusError(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter = ERROR_REQUEUE_TIMEOUT
	var stsStorage, stsStorageClassName string

	// If storage config is not consistent do not try to recover from Error.
	if !r.checkStorageConfigConsistency(ctx, redisCluster, false) {
		return requeue, requeueAfter
	}

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err == nil {
		// validate forbidden properties
		if sset != nil && sset.Spec.VolumeClaimTemplates != nil && len(sset.Spec.VolumeClaimTemplates) > 0 {
			stsStorage = sset.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()
			if sset.Spec.VolumeClaimTemplates[0].Spec.StorageClassName != nil {
				stsStorageClassName = *sset.Spec.VolumeClaimTemplates[0].Spec.StorageClassName
			}
		}
	}

	// Try to recover from Error status if storage configuration led to this but it has been now fixed.
	if redisCluster.Spec.Storage == stsStorage && redisCluster.Spec.StorageClassName == stsStorageClassName {
		r.CheckConfigurationStatus(ctx, redisCluster)
		err := r.ScaleCluster(ctx, redisCluster)
		if err == nil {
			err = r.UpdateScalingStatus(ctx, redisCluster)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
				r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			}
		} else {
			r.LogError(redisCluster.NamespacedName(), err, "Error when scaling cluster")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
		requeueAfter = DEFAULT_REQUEUE_TIMEOUT
	}

	// Check and update RedisCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redisCluster.Status.Status == redisv1.StatusError {
		err = r.UpdateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusError {
		err = r.UpdateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}

	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) getClusterNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) (kubernetes.ClusterNodeList, error) {
	clusterNodes, err := kubernetes.GetKubernetesClusterNodes(ctx, r.Client, redisCluster)
	if err != nil {
		return kubernetes.ClusterNodeList{}, err
	}
	err = clusterNodes.LoadInfoForNodes()
	if err != nil {
		return kubernetes.ClusterNodeList{}, err
	}
	return clusterNodes, nil
}

func (r *RedisClusterReconciler) freeClusterNodes(clusterNodes kubernetes.ClusterNodeList, RCNamespacedName types.NamespacedName) {
	for i := range clusterNodes.Nodes {
		err := kubernetes.FreeKubernetesClusterNode(clusterNodes.Nodes[i])
		if err != nil {
			// Log error and keep trying with the other nodes
			r.LogError(RCNamespacedName, err, "Error releasing cluster node")
		}
	}
}

func (r *RedisClusterReconciler) scaleUpAndWait(ctx context.Context, redisCluster *redisv1.RedisCluster) (int32, error) {
	// Add a new node to the cluster to make sure that there's enough space to move slots
	r.LogInfo(redisCluster.NamespacedName(), "Scaling up before the upgrade")
	// But first lets check if there is a pod dangling from a previous attempt that gone sour
	// For example if a non-existant redis image is requested, it'd get stuck on n+1th pod being never created successfuly and that pod
	// might be still there.

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error while getting StatefulSet")
		return 0, err
	}

	originalCount := redisCluster.Spec.Replicas
	if *sset.Spec.Replicas == originalCount {
		redisCluster.Spec.Replicas++
		err = r.ScaleCluster(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when scaling up")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
			return 0, err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Added a new pod, now we'll wait it to become ready")
	} else if *sset.Spec.Replicas == originalCount+1 {
		// Resume if the cluster was already scaled for upgrading.
		r.LogInfo(redisCluster.NamespacedName(), "Cluster already scaled, resume the processing")
		redisCluster.Spec.Replicas = *sset.Spec.Replicas
	}
	podReadyWaiter := utils.PodReadyWait{
		Client: r.Client,
	}
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.GetStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}
	r.LogInfo(redisCluster.NamespacedName(), "Waiting for pods to become ready", "expectedReplicas", int(redisCluster.Spec.Replicas))
	err = podReadyWaiter.WaitForPodsToBecomeReady(ctx, 5*time.Second, 5*time.Minute, &listOptions, int(redisCluster.Spec.Replicas))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error waiting for pods to become ready", "expectedReplicas", int(redisCluster.Spec.Replicas))
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		return 0, err
	}

	// All the pods are ready. Do they need a cluster meet ?
	nodes, err := r.GetReadyNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not fetch nodes for Cluster Meet check")
		return 0, err
	}
	needsMeet, _ := redis.NeedsClusterMeet(ctx, r.Client, nodes, redisCluster)
	if needsMeet {
		r.LogInfo(redisCluster.NamespacedName(), "ClusterMeet needed, starting")
		err = r.ClusterMeet(ctx, nodes, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not run ClusterMeet")
			return 0, err
		}
	}
	r.LogInfo(redisCluster.NamespacedName(), "ClusterMeet not needed, or finished")
	return originalCount, nil
}

func (r *RedisClusterReconciler) AllPodsReady(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.GetStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}
	podsReady, err := utils.AllPodsReady(ctx, r.Client, &listOptions, redisCluster.NodesNeeded())
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
		return false, err
	}
	return podsReady, nil
}

func (r *RedisClusterReconciler) RemoveClusterOutdatedNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// If a node is listed in the nodes.conf file of a Redis node,
	// but it is no longer applicable due to a restart and receiving a new IP from Kubernetes,
	// we can consider the Node outdated.
	//
	// We need to find any nodes that are outdated due to restart in ephemeral mode.
	//
	// If there are the correct amount of pods,
	// and the same amount of successful nodes,
	// but > 0 failing nodes,
	// we can safely assume that the failing node is due to a restart,
	// and optimistically delete it from the set of nodes in redis.
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.GetStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}
	podsReady, err := utils.AllPodsReady(ctx, r.Client, &listOptions, int(redisCluster.Spec.Replicas))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
		return err
	}

	if !podsReady {
		return nil
	}

	clusterNodes, err := kubernetes.GetKubernetesClusterNodes(ctx, r.Client, redisCluster)
	if err != nil {
		// Ready nodes will fetch all the Redis Cluster nodes which are ready.
		// It does a self-id to make sure the node is ready.
		// If we receive an error here, it means not all the nodes are ready
		return err
	}

	if len(clusterNodes.Nodes) != int(redisCluster.Spec.Replicas) {
		return nil
	}

	err = clusterNodes.LoadInfoForNodes()
	if err != nil {
		return err
	}

	for i := range clusterNodes.Nodes {
		for _, friend := range clusterNodes.Nodes[i].Friends() {
			if friend.HasFlag("fail") || friend.HasFlag("noaddr") {
				// If the node is outdated, we can forget it on all teh valid nodes
				for _, node := range clusterNodes.Nodes {
					_, err := node.ClusterNode.ClusterForgetNodeID(friend.NodeID())
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (r *RedisClusterReconciler) AssignMissingSlots(ctx context.Context, redisCluster *redisv1.RedisCluster) error {

	// First we need to get all the nodes
	// Then calculate which slots are assigned
	// We then need to subtract this from all the slots.
	// Then we need to loop over the unassigned slots,
	// and add them to the nodes with the least amount of assigned slots

	nodes, err := r.GetReadyNodes(ctx, redisCluster)
	if err != nil {
		return err
	}

	unassignedSlots := makeRangeMap(0, 16383)

	clusterNodes := map[string]*redis.ClusterNode{}
	defer r.releaseClusterNodes(clusterNodes, redisCluster.NamespacedName())
	for nodeId, node := range nodes {
		clusterNode, err := redis.NewClusterNode(fmt.Sprintf("%s:%d", node.IP, redis.RedisCommPort))
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to connect Cluster Node Info", "IP", node.IP)
			return err
		}
		err = clusterNode.LoadInfo(true)
		if err != nil {
			return err
		}
		clusterNodes[nodeId] = clusterNode

		for _, slot := range clusterNode.Slots() {
			// We want to remove any assigned slots from our list so we end up with unassigned slots
			delete(unassignedSlots, slot)
		}
	}
	var masterLen int
	for _, v := range clusterNodes {
		if v.HasFlag("master") {
			masterLen++
		}
	}

	slotsPerNode := kubernetes.CalculateMaxSlotsPerMaster(kubernetes.RedisClusterTotalSlots, masterLen)

	// We look for the slots to be assigned:
	// - Slots not already assigned to master nodes
	// - Keeping the slots not assigned but known by friends to treat them separately.
	//   Trying to assign these slots would end in a busy slot error.
	var slotsToAssign []int
	slotsKnown := make(map[string][]int)
	for slot := range unassignedSlots {
		slotKnown, nodeId := isSlotKnownByFriends(clusterNodes, slot)
		if slotKnown {
			slotsKnown[nodeId] = append(slotsKnown[nodeId], slot)
		}
		slotsToAssign = append(slotsToAssign, slot)
	}
	sort.Ints(slotsToAssign)

	// First, we delete all not assigned to master nodes slots known by their friends
	// to be able to assign them in the next step without errors.
	for _, node := range clusterNodes {
		if node.HasFlag("master") {
			slots, ok := slotsKnown[node.Info().NodeID()]
			if ok {
				err := clusterDelSlots(node, slots)
				if err != nil {
					return err
				}
			}
		}
	}

	for _, node := range clusterNodes {
		if node.HasFlag("master") {
			slotAmountToAssign := slotsPerNode - len(node.Slots())
			if slotAmountToAssign <= 0 {
				continue
			}
			// If the length of slots left is within 3 of the amount needed,
			// we can assume it's the last or only node left to get slots,
			// and assign all the remaining slots
			var slotList []int
			if len(slotsToAssign) <= (slotAmountToAssign + 15) {
				slotList = slotsToAssign[0:]
				slotsToAssign = []int{}
			} else {
				slotList = slotsToAssign[0:slotAmountToAssign]
				slotsToAssign = slotsToAssign[slotAmountToAssign:]
			}

			err := clusterAddSlots(node, slotList)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func clusterDelSlots(node *redis.ClusterNode, slotList []int) error {
	var strSlotList []interface{}
	for _, slot := range slotList {
		strSlotList = append(strSlotList, strconv.Itoa(slot))
	}

	if len(strSlotList) > 0 {
		_, err := node.ClusterDelSlots(strSlotList...)
		if err != nil {
			return err
		}
	}
	return nil
}

func clusterAddSlots(node *redis.ClusterNode, slotList []int) error {
	var strSlotList []interface{}
	for _, slot := range slotList {
		strSlotList = append(strSlotList, strconv.Itoa(slot))
	}

	if len(strSlotList) > 0 {
		_, err := node.ClusterAddSlots(strSlotList...)
		if err != nil {
			return err
		}
	}
	return nil
}

func isSlotKnownByFriends(clusterNodes map[string]*redis.ClusterNode, slot int) (bool, string) {
	for _, node := range clusterNodes {
		for _, friend := range node.Friends() {
			if slices.Contains(friend.Slots(), slot) {
				return true, node.Info().NodeID()
			}
		}
	}
	return false, ""
}

func (r *RedisClusterReconciler) releaseClusterNodes(clusterNodes map[string]*redis.ClusterNode, RCNamespacedName types.NamespacedName) {
	for i := range clusterNodes {
		err := redis.ReleaseClusterNode(clusterNodes[i])
		if err != nil {
			// Log error and keep trying with the other nodes
			r.LogError(RCNamespacedName, err, "Error releasing cluster node")
		}
	}
}

func (r *RedisClusterReconciler) EnsureClusterContention(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// First we need to get all the nodes
	// Then calculate which slots are assigned
	// We then need to subtract this from all the slots.
	// Then we need to loop over the unassigned slots,
	// and add them to the nodes with the least amount of assigned slots
	nodes, err := r.GetReadyNodes(ctx, redisCluster)
	if err != nil {
		return err
	}

	redisSecret, _ := r.GetRedisSecret(redisCluster)
	for _, node := range nodes {
		redisClient := r.GetRedisClient(ctx, node.IP, redisSecret)
		defer redisClient.Close()
		info, err := redisClient.Do(ctx, "INFO").Result()
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not get node info", "node", node)
			return err
		}
		if !strings.Contains(info.(string), "role:master") {
			// The node restarted without being a master node.
			_, err = redisClient.Do(ctx, "cluster", "reset").Result()
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Could not reset node")
				return err
			}
		}
	}
	return nil
}

func (r *RedisClusterReconciler) CheckClusterMeet(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// We might be reconciling from a pod restart.
	// We want to check whether the nodes all have the correct IPs for one another.
	// Due to an existing bug in Redis where a node doesn't update it's own IP upon restart,
	// we need to make sure to update all the nodes whenever a pod is rescheduled and receives a new IP.
	// First, let's check whether all the pods are ready
	listOptions := client.ListOptions{
		Namespace: redisCluster.Namespace,
		LabelSelector: labels.SelectorFromSet(
			map[string]string{
				redis.RedisClusterLabel:                     redisCluster.Name,
				r.GetStatefulSetSelectorLabel(redisCluster): "redis",
			},
		),
	}
	podsReady, err := utils.AllPodsReady(ctx, r.Client, &listOptions, int(redisCluster.Spec.Replicas))
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check for pods being ready")
	}
	if podsReady {
		// All the pods are ready. Do they need a cluster meet ?
		nodes, err := r.GetReadyNodes(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not fetch nodes for Cluster Meet check")
			return err
		}
		needsMeet, err := redis.NeedsClusterMeet(ctx, r.Client, nodes, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not determine if there was any change needed")
		}
		if needsMeet {
			err = r.ClusterMeet(ctx, nodes, redisCluster)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Could not run ClusterMeet")
				return err
			}
		}
	}
	return nil
}

func (r *RedisClusterReconciler) CheckConfigurationStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) {
	clusterInfo := r.GetClusterInfo(ctx, redisCluster)
	state := clusterInfo["cluster_state"]
	slots_ok := clusterInfo["cluster_slots_ok"]
	slots_assigned := clusterInfo["cluster_slots_assigned"]
	readyNodes, _ := r.GetReadyNodes(ctx, redisCluster)
	r.LogInfo(redisCluster.NamespacedName(), "CheckConfigurationStatus", "cluster_state", state, "cluster_slots_ok", slots_ok, "status", redisCluster.Status.Status, "clusterinfo", clusterInfo)
	if state == "ok" && slots_ok == strconv.Itoa(redis.TotalClusterSlots) {
		redisCluster.Status.Status = redisv1.StatusReady
	}
	if slots_ok == "0" || slots_ok == "" {
		if len(readyNodes) == redisCluster.NodesNeeded() {
			if redisCluster.Spec.Replicas == 0 {
				redisCluster.Status.Status = redisv1.StatusReady
			} else {
				redisCluster.Status.Status = redisv1.StatusConfiguring
			}
		} else {
			redisCluster.Status.Status = redisv1.StatusInitializing
		}
		return
	}
	if slots_ok != slots_assigned {
		redisCluster.Status.Status = redisv1.StatusConfiguring
		r.LogInfo(redisCluster.NamespacedName(), "CheckConfigurationStatus - slots assigned != slots  ok", "slots_ok", slots_ok, "slots_assigned", slots_assigned)
	}
}

// Checks if the RedisCluster needs a meet or a rebalance, when in Ready status. If any of both is needed, then the status is
// switched to Configuring to be meet o rebalanced.
func (r *RedisClusterReconciler) UpdateConfiguringStatusMeetOrBalance(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// Only when the cluster is in Ready status we check for the need of a meet or balance, switching to Configuring status if needed.
	if redisCluster.Status.Status != redisv1.StatusReady {
		return nil
	}

	// Checks if a node meet is needed.
	sset, ssetErr := r.FindExistingStatefulSetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if ssetErr != nil {
		return ssetErr
	}
	currSsetReplicas := *(sset.Spec.Replicas)
	clusterInfo := r.GetClusterInfo(ctx, redisCluster)
	clusterKnownNodes, csError := strconv.Atoi(clusterInfo["cluster_known_nodes"])

	// Nodes meet needed if known nodes not matching StatefulSet currect replicas. Entering Configuring status to fix it.
	if csError == nil && currSsetReplicas > int32(clusterKnownNodes) {
		redisCluster.Status.Status = redisv1.StatusConfiguring
		r.LogInfo(redisCluster.NamespacedName(), "CheckConfigurationStatus - current sset replicas > clusterKnownNodes", "currSsetReplicas", currSsetReplicas, "clusterKnownNodes", clusterKnownNodes)
	}

	// Checks if a cluster rebalance is needed.
	balanced, err := r.isClusterBalanced(ctx, redisCluster)
	if err != nil {
		return err
	}
	if !balanced {
		redisCluster.Status.Status = redisv1.StatusConfiguring
		r.LogInfo(redisCluster.NamespacedName(), "CheckConfigurationStatus - redis cluster unbalanced, switching to Configuring status to rebalance")
	}

	return nil
}

// Checks if a RedisCluster needs a meet, comparing the cluster_known_nodes from redis-cli cluster info value
// with the current replicas in the StatefulSet. Cluster meet is done if needed.
func (r *RedisClusterReconciler) CheckAndMeet(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	sset, ssetErr := r.FindExistingStatefulSetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if ssetErr != nil {
		return ssetErr
	}
	currSsetReplicas := *(sset.Spec.Replicas)

	clusterInfo := r.GetClusterInfo(ctx, redisCluster)
	clusterKnownNodes, csError := strconv.Atoi(clusterInfo["cluster_known_nodes"])

	// Nodes meet needed if known nodes not matching StatefulSet currect replicas
	if csError == nil && currSsetReplicas > int32(clusterKnownNodes) {
		r.LogInfo(redisCluster.NamespacedName(), "Cluster needs a nodes meet", "redis_cluster", redisCluster.Name, "cluster_known_nodes", clusterKnownNodes, "StatefulSet replicas", currSsetReplicas)
		clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
			return err
		}
		// Free cluster nodes to avoid memory consumption
		defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())
		if err = clusterNodes.ClusterMeet(ctx); err != nil {
			r.Recorder.Event(redisCluster, "Warning", "ClusterMeet", "Error when attempting ClusterMeet")
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Cluster meet completed", "redis_cluster", redisCluster.Name)
	}

	return nil
}

// Checks if a RedisCluster needs to be balanced and rebalances it when needed.
func (r *RedisClusterReconciler) CheckAndBalance(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// Check if the cluster is not balanced
	balanced, err := r.isClusterBalanced(ctx, redisCluster)
	if err != nil {
		return err
	}

	// Rebalance if needed
	if !balanced {
		r.LogInfo(redisCluster.NamespacedName(), "Cluster needs to be rebalanced", "redis_cluster", redisCluster.Name)
		clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
			return err
		}
		// Free cluster nodes to avoid memory consumption
		defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

		if err = r.ensureClusterRatioAndRebalance(ctx, redisCluster, clusterNodes); err != nil {
			r.Recorder.Event(redisCluster, "Warning", "ClusterRebalance", "Error when attempting ClusterRebalance")
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Cluster rebalance completed", "redis_cluster", redisCluster.Name)

	}

	return nil
}

// Checks if the cluster is balanced.
// If any of the nodes has no slots assigned or the number of slots assigned to the number of slots assigned
// to the nodes differs by more than the established rate, the cluster is considered unbalanced.
func (r *RedisClusterReconciler) isClusterBalanced(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {

	// Rebalance needed if any of the nodes has no slots assigned, no need to check anything else
	clusterInfo := r.GetClusterInfo(ctx, redisCluster)
	clusterSize, crError := strconv.Atoi(clusterInfo["cluster_size"])
	// Update clusterSize with replicasPerMaster before checking
	if redisCluster.Spec.ReplicasPerMaster > 0 {
		clusterSize = clusterSize + (clusterSize * int(redisCluster.Spec.ReplicasPerMaster))
	}
	realExpectedReplicas := int(redisCluster.NodesNeeded())
	if crError == nil && clusterSize < realExpectedReplicas {
		r.LogInfo(redisCluster.NamespacedName(), "Check cluster balance - one or more nodes has no slots assigned", "clusterSize", clusterSize, "realExpectedReplicas", realExpectedReplicas)
		return false, nil
	}

	clusterNodes, err := r.getClusterNodes(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not get cluster nodes")
		return false, err
	}
	// Free cluster nodes to avoid memory consumption
	defer r.freeClusterNodes(clusterNodes, redisCluster.NamespacedName())

	unbalancedNodes, err := clusterNodes.GetUnbalancedNodes(ctx)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check nodes balance")
		return false, err
	}
	if len(unbalancedNodes) == 0 {
		r.LogInfo(redisCluster.NamespacedName(), fmt.Sprintf("No rebalancing needed! All nodes are within the %d%% threshold", kubernetes.RedisNodesUnbalancedThreshold))
		return true, nil
	} else {
		for node, leftoverSlots := range unbalancedNodes {
			r.LogInfo(redisCluster.NamespacedName(), "Unbalanced node found! Has more than exepected assigned slots", "node", node, "leftover slots", leftoverSlots)
		}
		return false, nil
	}
}

func (r *RedisClusterReconciler) FindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return r.FindExistingStatefulSetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return kubernetes.FindExistingStatefulSet(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return r.FindExistingDeploymentFunc(ctx, req)
}

func (r *RedisClusterReconciler) FindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return r.FindExistingPodDisruptionBudgetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return kubernetes.FindExistingDeployment(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) DoFindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return kubernetes.FindExistingPodDisruptionBudget(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) GetPersistentVolumeClaim(ctx context.Context, client client.Client, redisCluster *redisv1.RedisCluster, name string) (*corev1.PersistentVolumeClaim, error) {
	r.LogInfo(redisCluster.NamespacedName(), "Getting persistent volume claim to be deleted ")
	pvc := &corev1.PersistentVolumeClaim{}
	err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: redisCluster.Namespace}, pvc)

	if err != nil {
		return nil, err
	}
	return pvc, nil
}

func (ef *RedisClusterReconciler) DeletePVC(ctx context.Context, client client.Client, pvc *corev1.PersistentVolumeClaim) error {
	err := client.Delete(ctx, pvc)
	return err
}

func (r *RedisClusterReconciler) DoFindExistingConfigMap(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	cmap := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, cmap)
	if err != nil {
		return nil, err
	}
	return cmap, nil
}

func (r *RedisClusterReconciler) FindExistingService(ctx context.Context, req ctrl.Request) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, svc)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *RedisClusterReconciler) CreateConfigMap(req ctrl.Request, spec redisv1.RedisClusterSpec, secret *corev1.Secret, labels map[string]string) *corev1.ConfigMap {
	newRedisClusterConf := redis.ConfigStringToMap(spec.Config)
	labels[redis.RedisClusterLabel] = req.Name
	labels[redis.RedisClusterComponentLabel] = "redis"
	if val, exists := secret.Data["requirepass"]; exists {
		newRedisClusterConf["requirepass"] = append(newRedisClusterConf["requirepass"], string(val))

	} else if secret.Name != "" {
		r.LogInfo(req.NamespacedName, "requirepass field not found in secret", "secretdata", secret.Data)
	}

	redisConfMap := redis.MergeWithDefaultConfig(newRedisClusterConf, spec.Ephemeral, spec.ReplicasPerMaster)

	redisConf := redis.MapToConfigString(redisConfMap)
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"redis.conf": redisConf, "memory-overhead": "300Mi"},
	}

	r.LogInfo(req.NamespacedName, "Generated Configmap", "configmap", cm)
	r.LogInfo(req.NamespacedName, "Spec config", "speconfig", spec.Config)
	return &cm
}

func (r *RedisClusterReconciler) CreateRobinDeployment(ctx context.Context, req ctrl.Request, rediscluster *redisv1.RedisCluster, labels map[string]string) *v1.Deployment {
	var replicas = int32(1)
	d := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Template: *rediscluster.Spec.Robin.Template,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{redis.RedisClusterLabel: req.Name, r.GetStatefulSetSelectorLabel(rediscluster): "robin"},
			},
			Replicas: &replicas,
		},
	}
	d.Labels[redis.RedisClusterLabel] = req.Name
	d.Labels[redis.RedisClusterComponentLabel] = "robin"
	d.Spec.Template.Labels = make(map[string]string)
	maps.Copy(d.Spec.Template.Labels, labels)
	d.Spec.Template.Labels[redis.RedisClusterLabel] = req.Name
	d.Spec.Template.Labels[redis.RedisClusterComponentLabel] = "robin"
	maps.Copy(d.Spec.Template.Labels, rediscluster.Spec.Robin.Template.Labels)

	for i, container := range d.Spec.Template.Spec.Containers {
		if container.Resources.Requests == nil {
			d.Spec.Template.Spec.Containers[i].Resources.Requests = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("20m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
		if container.Resources.Limits == nil {
			d.Spec.Template.Spec.Containers[i].Resources.Limits = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			}
		}
	}

	return d
}

func (r *RedisClusterReconciler) CreateRobinConfigMap(req ctrl.Request, spec redisv1.RedisClusterSpec, labels map[string]string) *corev1.ConfigMap {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name + "-robin",
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"application-configmap.yml": *spec.Robin.Config},
	}

	r.LogInfo(req.NamespacedName, "Generated robin configmap")
	return &cm
}

func (r *RedisClusterReconciler) CreatePodDisruptionBudget(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, labels map[string]string) *pv1.PodDisruptionBudget {
	pdb := &pv1.PodDisruptionBudget{}
	if redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal != "" || redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal != 0 {
		pdb = &pv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name + "-pdb",
				Namespace: req.Namespace,
				Labels:    labels,
			},
			Spec: pv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &redisCluster.Spec.Pdb.PdbSizeUnavailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{redis.RedisClusterLabel: req.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"},
				},
			},
		}
	} else {
		pdb = &pv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name + "-pdb",
				Namespace: req.Namespace,
				Labels:    labels,
			},
			Spec: pv1.PodDisruptionBudgetSpec{
				MinAvailable: &redisCluster.Spec.Pdb.PdbSizeAvailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{redis.RedisClusterLabel: req.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"},
				},
			},
		}
	}

	return pdb
}

func (r *RedisClusterReconciler) CreateStatefulSet(ctx context.Context, req ctrl.Request, spec redisv1.RedisClusterSpec, labels map[string]string, annotations map[string]string, configmap *corev1.ConfigMap) (*v1.StatefulSet, error) {
	statefulSet, err := redis.CreateStatefulSet(ctx, req, spec, labels)
	if err != nil {
		return statefulSet, err
	}

	// Add labels to statefulset and its template if provided
	if spec.Labels != nil {
		for k, v := range *spec.Labels {
			statefulSet.Labels[k] = v
			statefulSet.Spec.Template.Labels[k] = v
		}
	}

	// Set resources if provided
	inferResources := true
	if spec.Resources != nil {
		inferResources = false
		for k := range statefulSet.Spec.Template.Spec.Containers {
			statefulSet.Spec.Template.Spec.Containers[k].Resources = *spec.Resources
		}
	}

	// Override the statefulset with the provided override
	if spec.Override != nil && spec.Override.StatefulSet != nil {
		patchedStatefulSet, err := redis.ApplyStsOverride(statefulSet, spec.Override.StatefulSet)
		if err != nil {
			return nil, err
		}
		statefulSet = patchedStatefulSet

		// Check if the override has resources
		if len(spec.Override.StatefulSet.Spec.Template.Spec.Containers) > 0 && (spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Resources.Requests != nil || spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Resources.Limits != nil) {
			inferResources = false
		}
	}

	// Infer resources if needed
	if inferResources {
		statefulSetWithInferredResources, err := r.inferResources(req, spec, configmap, statefulSet)
		if err != nil {
			return nil, err
		}
		statefulSet = statefulSetWithInferredResources
	}

	return statefulSet, nil
}

func (r *RedisClusterReconciler) inferResources(req ctrl.Request, spec redisv1.RedisClusterSpec, configmap *corev1.ConfigMap, statefulSet *v1.StatefulSet) (*v1.StatefulSet, error) {
	config := spec.Config
	desiredConfig := redis.MergeWithDefaultConfig(
		redis.ConfigStringToMap(config),
		spec.Ephemeral,
		spec.ReplicasPerMaster)

	maxMemoryInt, err := redis.ExtractMaxMemory(desiredConfig)
	if err != nil {
		return nil, err
	}

	r.LogInfo(req.NamespacedName, "Merged config", "withDefaults", desiredConfig)

	memoryOverheadConfig := configmap.Data["maxmemory-overhead"]
	var memoryOverheadResource resource.Quantity

	if memoryOverheadConfig == "" {
		memoryOverheadResource = resource.MustParse("300Mi")
	} else {
		memoryOverheadResource = resource.MustParse(memoryOverheadConfig)
	}

	memoryLimit, _ := resource.ParseQuantity(fmt.Sprintf("%dMi", maxMemoryInt)) // add 300 mb from config maxmemory
	cpuLimit, _ := resource.ParseQuantity("1")
	r.LogInfo(req.NamespacedName, "New memory limits", "memory", memoryLimit)
	memoryLimit.Add(memoryOverheadResource)
	for k := range statefulSet.Spec.Template.Spec.Containers {
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceMemory] = memoryLimit
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory] = memoryLimit
		r.LogInfo(req.NamespacedName, "Stateful set container memory", "memory", statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceMemory])

		statefulSet.Spec.Template.Spec.Containers[k].Resources.Requests[corev1.ResourceCPU] = cpuLimit
		statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU] = cpuLimit
		r.LogInfo(req.NamespacedName, "Stateful set cpu", "cpu", statefulSet.Spec.Template.Spec.Containers[k].Resources.Limits[corev1.ResourceCPU])

	}
	return statefulSet, nil
}

func (r *RedisClusterReconciler) OverrideStatefulSet(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, statefulSet *v1.StatefulSet) (*v1.StatefulSet, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redisCluster.Spec.Override == nil {
		redisCluster.Spec.Override = &redisv1.RedisClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redisCluster.Spec.Override.StatefulSet == nil {
		redisCluster.Spec.Override.StatefulSet = &v1.StatefulSet{}
	}

	// Apply the override
	patchedStatefulSet, err := redis.ApplyStsOverride(statefulSet, redisCluster.Spec.Override.StatefulSet)
	if err != nil {
		ctrl.Log.Error(err, "Error applying StatefulSet override")
		return statefulSet, false
	}

	// Apply the resources override if provided in containers without resources after patch
	if redisCluster.Spec.Resources != nil {
		for k := range patchedStatefulSet.Spec.Template.Spec.Containers {
			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Limits = redisCluster.Spec.Resources.Limits
			}

			if patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests == nil {
				patchedStatefulSet.Spec.Template.Spec.Containers[k].Resources.Requests = redisCluster.Spec.Resources.Requests
			}
		}
	}

	// Check if the override changes something in the original StatefulSet
	changed := !reflect.DeepEqual(statefulSet.Labels, patchedStatefulSet.Labels) || !reflect.DeepEqual(statefulSet.Annotations, patchedStatefulSet.Annotations) || !reflect.DeepEqual(statefulSet.Spec, patchedStatefulSet.Spec)

	if changed {
		r.LogInfo(req.NamespacedName, "Detected StatefulSet override change")
	} else {
		r.LogInfo(req.NamespacedName, "No StatefulSet override change detected")
	}

	return patchedStatefulSet, changed
}

func (r *RedisClusterReconciler) CreateService(req ctrl.Request, spec redisv1.RedisClusterSpec, labels map[string]string) *corev1.Service {
	service := redis.CreateService(req.Namespace, req.Name, labels)

	// Override the service with the provided override
	if spec.Override != nil && spec.Override.Service != nil {
		patchedService, err := redis.ApplyServiceOverride(service, spec.Override.Service)
		if err != nil {
			return service
		}
		service = patchedService
	}

	return service
}

func (r *RedisClusterReconciler) OverrideService(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, service *corev1.Service) (*corev1.Service, bool) {
	// Create a default override if it doesn't exist. This is to handle the case where the user removes the override of an existing cluster.
	if redisCluster.Spec.Override == nil {
		redisCluster.Spec.Override = &redisv1.RedisClusterOverrideSpec{
			StatefulSet: &v1.StatefulSet{},
			Service:     &corev1.Service{},
		}
	} else if redisCluster.Spec.Override.Service == nil {
		redisCluster.Spec.Override.Service = &corev1.Service{}
	}

	// Apply the override
	patchedService, err := redis.ApplyServiceOverride(service, redisCluster.Spec.Override.Service)
	if err != nil {
		ctrl.Log.Error(err, "Error applying Service override")
		return service, false
	}

	// Check if the override changes something in the original Service
	changed := !reflect.DeepEqual(service.Labels, patchedService.Labels) || !reflect.DeepEqual(service.Annotations, patchedService.Annotations) || !reflect.DeepEqual(service.Spec, patchedService.Spec)

	if changed {
		r.LogInfo(req.NamespacedName, "Detected service override change")
	} else {
		r.LogInfo(req.NamespacedName, "No service override change detected")
	}

	return patchedService, changed
}

func (r *RedisClusterReconciler) GetSecret(ctx context.Context, ns types.NamespacedName, RCNamespacedName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, ns, secret)
	if err != nil {
		r.LogError(RCNamespacedName, err, "Getting secret failed", "secret", ns)
	}
	return secret, err
}

func (r *RedisClusterReconciler) GetClusterInfo(ctx context.Context, redisCluster *redisv1.RedisCluster) map[string]string {
	return r.GetClusterInfoFunc(ctx, redisCluster)
}

func (r *RedisClusterReconciler) DoGetClusterInfo(ctx context.Context, redisCluster *redisv1.RedisCluster) map[string]string {
	if len(redisCluster.Status.Nodes) == 0 {
		r.LogInfo(redisCluster.NamespacedName(), "No ready nodes available on the cluster.", "clusterinfo", map[string]string{})
		return map[string]string{}
	}
	nodes := r.GetRedisClusterPods(ctx, redisCluster)
	if len(nodes.Items) == 0 {
		return nil
	}
	secret, _ := r.GetRedisSecret(redisCluster)
	rdb := r.GetRedisClient(ctx, nodes.Items[0].Status.PodIP, secret)
	info, _ := rdb.ClusterInfo(ctx).Result()
	parsedClusterInfo := redis.GetClusterInfo(info)
	rdb.Close()
	return parsedClusterInfo
}

func (r *RedisClusterReconciler) UpdateClusterStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	var req reconcile.Request
	req.NamespacedName.Namespace = redisCluster.Namespace
	req.NamespacedName.Name = redisCluster.Name

	r.LogInfo(redisCluster.NamespacedName(), "Updating cluster status", "status", redisCluster.Status.Status, "nodes", redisCluster.Status.Nodes)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		time.Sleep(time.Second * 1)
		// get a fresh rediscluster to minimize conflicts
		refreshedRedisCluster := redisv1.RedisCluster{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedRedisCluster.Status.Nodes = redisCluster.Status.Nodes
		refreshedRedisCluster.Status.Status = redisCluster.Status.Status
		refreshedRedisCluster.Status.Conditions = redisCluster.Status.Conditions
		refreshedRedisCluster.Status.Substatus = redisCluster.Status.Substatus

		var updateErr = r.Client.Status().Update(ctx, &refreshedRedisCluster)
		return updateErr
	})
}

func containsString(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

// GetStatefulSetSelectorLabel returns the label key that should be used to find RedisCluster nodes for
// backwards compatibility with the old version RedisCluster.
// The implementation has been moved, and this exists merely as an alias for backward compatibility
func (r *RedisClusterReconciler) GetStatefulSetSelectorLabel(rdcl *redisv1.RedisCluster) string {
	return kubernetes.GetStatefulSetSelectorLabel(context.TODO(), r.Client, rdcl)
}

func (r *RedisClusterReconciler) CheckAndCreateK8sObjects(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, auth *corev1.Secret) (bool, error) {
	var immediateRequeue bool = false
	var err error = nil

	// RedisCluster check
	err = r.CheckAndUpdateRDCL(ctx, req.Name, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error checking RedisCluster object")
		return immediateRequeue, err
	}

	// ConfigMap check
	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		if errors.IsNotFound(err) {
			configMap = r.CreateConfigMap(req, redisCluster.Spec, auth, redisCluster.GetObjectMeta().GetLabels())
			ctrl.SetControllerReference(redisCluster, configMap, r.Scheme)
			r.LogInfo(redisCluster.NamespacedName(), "Creating configmap", "configmap", configMap.Name)
			createMapErr := r.Client.Create(ctx, configMap)
			if createMapErr != nil {
				r.LogError(redisCluster.NamespacedName(), createMapErr, "Error when creating configmap")
				return immediateRequeue, createMapErr
			}
		} else {
			r.LogError(redisCluster.NamespacedName(), err, "Getting configmap data failed")
			return immediateRequeue, err
		}
	}

	// PodDisruptionBudget check
	if redisCluster.Spec.Pdb.Enabled && redisCluster.Spec.Replicas > 1 {
		_, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err != nil {
			if errors.IsNotFound(err) {
				// Create PodDisruptionBudget
				pdb := r.CreatePodDisruptionBudget(ctx, req, redisCluster, *redisCluster.Spec.Labels)
				ctrl.SetControllerReference(redisCluster, pdb, r.Scheme)
				pdbCreateErr := r.Client.Create(ctx, pdb)
				if pdbCreateErr != nil {
					r.LogError(redisCluster.NamespacedName(), pdbCreateErr, "Error creating PodDisruptionBudget")
				}
			} else {
				r.LogError(redisCluster.NamespacedName(), err, "Error getting existing PodDisruptionBudget")
			}
		}
	}
	if redisCluster.Spec.Replicas == 1 || !redisCluster.Spec.Pdb.Enabled {
		r.deletePdb(ctx, redisCluster)
	}

	// StatefulSet check
	statefulSet, err := r.FindExistingStatefulSet(ctx, req)
	var createSsetError error
	if err != nil {
		if errors.IsNotFound(err) {
			// Create StatefulSet
			r.LogInfo(redisCluster.NamespacedName(), "Creating statefulset")
			statefulSet, createSsetError = r.CreateStatefulSet(ctx, req, redisCluster.Spec, *redisCluster.Spec.Labels, redisCluster.ObjectMeta.Annotations, configMap)
			if createSsetError != nil {
				r.LogError(redisCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				return immediateRequeue, createSsetError
			}
			r.LogInfo(redisCluster.NamespacedName(), "Successfully created statefulset")

			// Set the config checksum annotation
			statefulSet = r.addConfigChecksumAnnotation(statefulSet, redisCluster)

			ctrl.SetControllerReference(redisCluster, statefulSet, r.Scheme)
			createSsetError = r.Client.Create(ctx, statefulSet)
			if createSsetError != nil {
				if errors.IsAlreadyExists(createSsetError) {
					r.LogInfo(redisCluster.NamespacedName(), "StatefulSet already exists")
				} else {
					r.LogError(redisCluster.NamespacedName(), createSsetError, "Error when creating StatefulSet")
				}
			}
		} else {
			r.LogError(redisCluster.NamespacedName(), err, "Getting statefulset data failed", "statefulset", statefulSet.Name)
			return immediateRequeue, err
		}
	} else {
		// Check StatefulSet <-> RedisCluster coherence
		readyNodes, err := r.GetReadyNodes(ctx, redisCluster)
		if err != nil {
			return immediateRequeue, err
		}
		currSsetReplicas := *(statefulSet.Spec.Replicas)
		currReadyNodes := int32(len(readyNodes))
		realExpectedReplicas := int32(redisCluster.NodesNeeded())
		if currSsetReplicas != currReadyNodes {
			immediateRequeue = false
			var err error
			if realExpectedReplicas < currSsetReplicas {
				// Inconsistency: if a scaleup could not be completed because of a lack of resources that prevented
				// all the needed pods from being created
				// StatefulSet replicas are then aligned with RedisCluster replicas
				r.LogInfo(redisCluster.NamespacedName(), "Not all required pods instantiated: aligning StatefulSet <-> RedisCluster replicas",
					"StatefulSet replicas", currSsetReplicas, "Ready nodes", len(readyNodes), "RedisCluster replicas", realExpectedReplicas)
				statefulSet.Spec.Replicas = &realExpectedReplicas
				_, err = r.UpdateStatefulSet(ctx, statefulSet, redisCluster)
				if err != nil {
					r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
				}
			} else {
				r.LogInfo(redisCluster.NamespacedName(), "StatefulSet - Not all pods Ready", "StatefulSet replicas", currSsetReplicas,
					"Ready nodes", currReadyNodes, "RedisCluster replicas", realExpectedReplicas)
				immediateRequeue = true
			}
			return immediateRequeue, err // requeue
		}
	}

	// Robin deployment check
	r.CheckAndCreateRobin(ctx, req, redisCluster)

	// Service check
	immediateRequeue, err = r.checkAndCreateService(ctx, req, redisCluster)

	return immediateRequeue, err
}

func (r *RedisClusterReconciler) CheckAndCreateRobin(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Populate robin spec if not provided. This to handle the case where the user removes the robin spec of an existing cluster. The robin objects will be deleted.
	if redisCluster.Spec.Robin == nil {
		redisCluster.Spec.Robin = &redisv1.RobinSpec{
			Config:   nil,
			Template: nil,
		}
	}

	// Robin configmap
	r.handleRobinConfig(ctx, req, redisCluster)

	// Robin deployment
	r.handleRobinDeployment(ctx, req, redisCluster)
}

func (r *RedisClusterReconciler) handleRobinConfig(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Get robin configmap
	configmap, err := r.FindExistingConfigMapFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})

	// Robin configmap not provided: delete configmap if exists
	if redisCluster.Spec.Robin.Config == nil {
		r.deleteRobinObject(ctx, configmap, redisCluster, "configmap")
		return
	}

	// Robin configmap not found: create configmap
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Getting robin configmap failed")
			return
		}

		// Create Robin ConfigMap
		mcm := r.CreateRobinConfigMap(req, redisCluster.Spec, *redisCluster.Spec.Labels)
		r.createRobinObject(ctx, mcm, redisCluster, "configmap")
		return
	}

	// Robin configmap found: check if it needs to be updated
	if *redisCluster.Spec.Robin.Config == configmap.Data["application-configmap.yml"] {
		return
	}

	// Robin configmap changed: update configmap
	configmap.Data["application-configmap.yml"] = *redisCluster.Spec.Robin.Config
	r.updateRobinObject(ctx, configmap, redisCluster, "configmap")

	// Add checksum to the deployment annotations to force the deployment rollout
	if redisCluster.Spec.Robin.Template != nil {
		if redisCluster.Spec.Robin.Template.Annotations == nil {
			redisCluster.Spec.Robin.Template.Annotations = make(map[string]string)
		}
		redisCluster.Spec.Robin.Template.Annotations["checksum/config"] = fmt.Sprintf("%x", md5.Sum([]byte(*redisCluster.Spec.Robin.Config)))
	}
}

func (r *RedisClusterReconciler) handleRobinDeployment(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	// Get robin deployment
	deployment, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})

	// Robin deployment template not provided: delete deployment if exists
	if redisCluster.Spec.Robin.Template == nil {
		r.deleteRobinObject(ctx, deployment, redisCluster, "deployment")
		return
	}

	// Robin deployment not found: create deployment
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Getting robin deployment failed")
			return
		}

		// Create Robin Deployment
		mdep := r.CreateRobinDeployment(ctx, req, redisCluster, *redisCluster.Spec.Labels)
		r.createRobinObject(ctx, mdep, redisCluster, "deployment")
		return
	}

	// Robin deployment found: check if it needs to be updated
	patchedPodTemplateSpec, changed := r.OverrideRobinDeployment(ctx, req, redisCluster, deployment.Spec.Template)
	if !changed {
		return
	}

	// Robin deployment changed: update deployment
	deployment.Spec.Template = patchedPodTemplateSpec
	r.updateRobinObject(ctx, deployment, redisCluster, "deployment")
}

func (r *RedisClusterReconciler) createRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	ctrl.SetControllerReference(redisCluster, obj, r.Scheme)

	r.LogInfo(redisCluster.NamespacedName(), "Creating robin "+kind)
	err := r.Client.Create(ctx, obj)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Error creating robin "+kind)
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "robin "+kind+" already exists")
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully created robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) updateRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Updating robin "+kind)
	err := r.Client.Update(ctx, obj)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error updating robin "+kind)
		return err
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully updated robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) deleteRobinObject(ctx context.Context, obj client.Object, redisCluster *redisv1.RedisCluster, kind string) error {
	if obj.DeepCopyObject() == nil {
		return nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "Deleting robin "+kind)
	err := r.Client.Delete(ctx, obj)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error deleting robin "+kind)
		return err
	}

	r.LogInfo(redisCluster.NamespacedName(), "Successfully deleted robin "+kind, kind, redisCluster.Name+"-robin")
	return nil
}

func (r *RedisClusterReconciler) OverrideRobinDeployment(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, podTemplateSpec corev1.PodTemplateSpec) (corev1.PodTemplateSpec, bool) {
	// Apply the override
	patchedPodTemplateSpec, err := redis.ApplyPodTemplateSpecOverride(podTemplateSpec, *redisCluster.Spec.Robin.Template)
	if err != nil {
		ctrl.Log.Error(err, "Error applying pod template spec override")
		return podTemplateSpec, false
	}

	// Check if the override changes something in the original PodTemplateSpec
	changed := !reflect.DeepEqual(podTemplateSpec.Labels, patchedPodTemplateSpec.Labels) || !reflect.DeepEqual(podTemplateSpec.Annotations, patchedPodTemplateSpec.Annotations) || !reflect.DeepEqual(podTemplateSpec.Spec, patchedPodTemplateSpec.Spec)

	if changed {
		r.LogInfo(req.NamespacedName, "Detected robin deployment change")
	} else {
		r.LogInfo(req.NamespacedName, "No robin deployment change detected")
	}

	return *patchedPodTemplateSpec, changed
}

func (r *RedisClusterReconciler) checkAndCreateService(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) (bool, error) {
	service, err := r.FindExistingService(ctx, req)
	if err != nil {
		// Return if the error is not a NotFound error
		if !errors.IsNotFound(err) {
			r.LogError(redisCluster.NamespacedName(), err, "Getting svc data failed")
			return false, err
		}

		// Create service otherwise
		r.LogInfo(redisCluster.NamespacedName(), "Creating service")
		service := r.CreateService(req, redisCluster.Spec, redisCluster.GetObjectMeta().GetLabels())
		ctrl.SetControllerReference(redisCluster, service, r.Scheme)

		createSVCError := r.Client.Create(ctx, service)
		if createSVCError != nil {
			if !errors.IsAlreadyExists(createSVCError) {
				return false, createSVCError
			}

			r.LogInfo(redisCluster.NamespacedName(), "Svc already exists")
			return false, nil
		}

		r.LogInfo(redisCluster.NamespacedName(), "Successfully created service", "service", service.Name)
	} else {
		// Handle changes in spec.override.service
		patchedService, changed := r.OverrideService(ctx, req, redisCluster, service)

		// Update service if changed
		if changed {
			service = patchedService
			err := r.Client.Update(ctx, service)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Error updating service")
				return false, err
			}
			r.LogInfo(redisCluster.NamespacedName(), "Successfully updated service", "service", service.Name)
		}
	}
	return false, nil
}

func (r *RedisClusterReconciler) CheckAndUpdateRDCL(ctx context.Context, redisName string, redisCluster *redisv1.RedisCluster) error {
	// Label redis.RedisClusterLabel needed by Redis backup
	if _, ok := redisCluster.Labels[redis.RedisClusterLabel]; !ok {
		r.LogInfo(redisCluster.NamespacedName(), "RDCL object not containing label", "label", redis.RedisClusterLabel)
		redisCluster.ObjectMeta.Labels[redis.RedisClusterLabel] = redisName
		if err := r.Update(ctx, redisCluster); err != nil {
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Label added to RedisCluster labels", "label", redis.RedisClusterLabel, "value", redisName)
	}
	// Label redis.RedisClusterComponentLabel needed by Redis backup
	if _, ok := redisCluster.Labels[redis.RedisClusterComponentLabel]; !ok {
		r.LogInfo(redisCluster.NamespacedName(), "RDCL object not containing label", "label", redis.RedisClusterComponentLabel)
		redisCluster.ObjectMeta.Labels[redis.RedisClusterComponentLabel] = "redis"
		if err := r.Update(ctx, redisCluster); err != nil {
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Label added to RedisCluster labels", "label", redis.RedisClusterComponentLabel, "value", "redis")
	}
	return nil
}

// Redis cluster is set to 0 replicas
//
//	 -> terminate all cluster pods (StatefulSet replicas set to 0)
//	 -> terminate robin pod (Deployment replicas set to 0)
//	 -> delete pdb
//	 -> Redis cluster status set to 'Ready'
//		-> All conditions set to false
func (r *RedisClusterReconciler) clusterScaledToZeroReplicas(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	r.LogInfo(redisCluster.NamespacedName(), "Cluster spec replicas is set to 0", "SpecReplicas", redisCluster.Spec.Replicas)
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Cannot find exists statefulset maybe is deleted.")
	}
	if sset != nil {
		if *(sset.Spec.Replicas) != 0 {
			r.LogInfo(redisCluster.NamespacedName(), "Cluster scaled to 0 replicas")
			r.Recorder.Event(redisCluster, "Normal", "RedisClusterScaledToZero", fmt.Sprintf("Scaling down from %d to 0", *(sset.Spec.Replicas)))
		}
		*sset.Spec.Replicas = 0
		sset, err = r.UpdateStatefulSet(ctx, sset, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		}
		r.LogInfo(redisCluster.NamespacedName(), "StatefulSet updated", "Replicas", sset.Spec.Replicas)
	}

	// Robin deployment update
	if redisCluster.Spec.Robin != nil {
		if redisCluster.Spec.Robin.Template != nil {
			mdep, err := r.FindExistingDeployment(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-robin", Namespace: redisCluster.Namespace}})
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Cannot find existing robin deployment", "deployment", redisCluster.Name+"-robin")
			} else {
				// Scaledown
				*mdep.Spec.Replicas = 0
				mdep, err = r.UpdateDeployment(ctx, mdep, redisCluster)
				if err != nil {
					r.LogError(redisCluster.NamespacedName(), err, "Failed to update Deployment replicas")
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Robin Deployment updated", "Replicas", mdep.Spec.Replicas)
				}
			}
		}
	}

	r.deletePdb(ctx, redisCluster)

	// All conditions set to false. Status set to Ready.
	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, redisv1.StatusReady) {
		redisCluster.Status.Status = redisv1.StatusReady
		SetAllConditionsFalse(r.GetHelperLogger(redisCluster.NamespacedName()), redisCluster)
		update_err = r.UpdateClusterStatus(ctx, redisCluster)
	}

	return update_err
}
