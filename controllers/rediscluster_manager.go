// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
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
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

	reconciler.GetReadyNodesFunc = reconciler.DoGetReadyNodes
	reconciler.FindExistingStatefulSetFunc = reconciler.DoFindExistingStatefulSet
	reconciler.FindExistingConfigMapFunc = reconciler.DoFindExistingConfigMap
	reconciler.FindExistingDeploymentFunc = reconciler.DoFindExistingDeployment
	reconciler.FindExistingPodDisruptionBudgetFunc = reconciler.DoFindExistingPodDisruptionBudget

	return reconciler
}

func (r *RedisClusterReconciler) ReconcileClusterObject(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) (ctrl.Result, error) {
	var pvcFinalizer = (&finalizer.DeletePVCFinalizer{}).GetId()
	var err error
	var requeueAfter time.Duration = DEFAULT_REQUEUE_TIMEOUT
	currentStatus := redisCluster.Status

	r.LogInfo(redisCluster.NamespacedName(), "RedisCluster reconciler start", "status", redisCluster.Status.Status)

	// Checks the existance of the ConfigMap, StatefulSet, Pods, Robin Deployment, PDB and Service,
	// creating the objects not created yet.
	// Coherence and configuration details are also checked and fixed.
	immediateRequeue, err := r.checkAndCreateK8sObjects(ctx, req, redisCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if immediateRequeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, err
	}

	// Check storage configuration consistency, updating status if needed.
	r.checkStorageConfigConsistency(ctx, redisCluster, redisCluster.Status.Status != redisv1.StatusError)

	// Redis cluster scaled to 0 replicas?
	// If it's a newly deployed cluster it won't have a status set yet and won't be catched here.
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
		r.LogInfo(redisCluster.NamespacedName(), "Delete PVCs feature disabled in cluster spec or not specified. PVCs won't be deleted after scaling down or cluster deletion")
	}

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

	requeue := false
	switch redisCluster.Status.Status {
	case "":
		requeue, requeueAfter = r.reconcileStatusNew(redisCluster)
	case redisv1.StatusInitializing:
		requeue, requeueAfter = r.reconcileStatusInitializing(ctx, redisCluster)
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
		r.LogError(redisCluster.NamespacedName(), nil, "Status not allowd", "status", redisCluster.Status.Status)
		return ctrl.Result{}, nil
	}

	r.LogInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)

	// AMZ update nodes info in status

	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, currentStatus) {
		update_err = r.updateClusterStatus(ctx, redisCluster)
	}

	if requeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, update_err
	}
	return ctrl.Result{}, update_err
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

func (r *RedisClusterReconciler) reconcileStatusNew(redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	r.LogInfo(redisCluster.NamespacedName(), "New RedisCluster. Initializing...")
	if redisCluster.Status.Nodes == nil {
		redisCluster.Status.Nodes = make(map[string]*redisv1.RedisNode, 0)
	}
	redisCluster.Status.Status = redisv1.StatusInitializing
	return requeue, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusInitializing(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {

	// Check Redis nod pods rediness
	nodePodsReady, err := r.AllPodsReady(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	if !nodePodsReady {
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	r.LogInfo(redisCluster.NamespacedName(), "Redis node pods are ready")

	// Check Robin pod readiness
	logger := r.GetHelperLogger(redisCluster.NamespacedName())
	robin, err := kubernetes.GetRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	flag, err := utils.PodRunningReady(robin.Pod)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error checking Robin pod readiness")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	if !flag {
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for Robin pod to become ready")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}

	// Check Robin is responding to requests
	status, err := robin.GetStatus()
	if err != nil {
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for Robin accepting requests")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	r.LogInfo(redisCluster.NamespacedName(), "Status", "status", status)

	// Redis pods and Robin are ok, moving to Configuring status
	redisCluster.Status.Status = redisv1.StatusConfiguring

	return false, DEFAULT_REQUEUE_TIMEOUT
}

func (r *RedisClusterReconciler) reconcileStatusConfiguring(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true

	// Ask Robin for Redis cluster readiness
	logger := r.GetHelperLogger((redisCluster.NamespacedName()))
	robin, err := kubernetes.GetRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error getting Robin to check the cluster readiness")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	check, errors, warnings, err := robin.ClusterCheck()
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return true, DEFAULT_REQUEUE_TIMEOUT
	}
	
	if !check {
		r.LogInfo(redisCluster.NamespacedName(), "Waiting for Redis cluster readiness", "errors", errors, "warnings", warnings)
		return true, DEFAULT_REQUEUE_TIMEOUT
	}

	// Redis cluster is ok, moving to Ready status
	redisCluster.Status.Status = redisv1.StatusReady

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

	// Reconcile PDB
	err = r.checkAndUpdatePodDisruptionBudget(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error checking PDB changes")
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

func (r *RedisClusterReconciler) scaleUpForUpgrade(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, error) {
	// Add a new node to the cluster to make sure that there's enough space to move slots
	// But first lets check if there is a pod dangling from a previous attempt that gone sour
	// For example if a non-existant redis image is requested, it'd get stuck on n+1th pod being never created successfuly and that pod
	// might be still there.

	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error while getting StatefulSet")
		return false, err
	}

	originalCount := redisCluster.Spec.Replicas
	if *sset.Spec.Replicas == originalCount && redisCluster.Status.Substatus.Status == "" {
		redisCluster.Spec.Replicas++
		err = r.ScaleCluster(ctx, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error when scaling up")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
			return false, err
		}
		err = r.updateSubStatus(ctx, redisCluster, redisv1.SubstatusScalingUp, 0)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error updating substatus")
			return false, err
		}
	} else if *sset.Spec.Replicas == originalCount+1 {
		// Resume if the cluster was already scaled for upgrading.
		r.LogInfo(redisCluster.NamespacedName(), "Cluster already scaled, resume the processing")
		redisCluster.Spec.Replicas = *sset.Spec.Replicas
	}

	podsReady, err := r.AllPodsReady(ctx, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Could not check for pods readiness")
		return false, err
	}
	if !podsReady {
		// pods not ready yet, return to requeue and keep waiting
		return false, nil
	}

	return true, nil
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

func (r *RedisClusterReconciler) FindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return r.FindExistingStatefulSetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return kubernetes.FindExistingStatefulSet(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return r.FindExistingDeploymentFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return kubernetes.FindExistingDeployment(ctx, r.Client, req)
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
		Data: map[string]string{"redis.conf": redisConf},
	}

	r.LogInfo(req.NamespacedName, "Generated Configmap", "configmap", cm)
	r.LogInfo(req.NamespacedName, "Spec config", "speconfig", spec.Config)
	return &cm
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

func (r *RedisClusterReconciler) updateClusterStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	var req reconcile.Request
	req.NamespacedName.Namespace = redisCluster.Namespace
	req.NamespacedName.Name = redisCluster.Name

	r.LogInfo(redisCluster.NamespacedName(), "New cluster status", "status", redisCluster.Status.Status)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		time.Sleep(time.Second * 1)

		// Update RedisCluster status first

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

		err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error updating RedisCluster object with new status")
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "RedisCluster has been updated with the new status", "status", redisCluster.Status.Status)

		// Update Robin status
		// Do not update if we are switching to Initializing status because Robin needs some
		// time to be ready to accept API requests.
		if redisCluster.Status.Status != redisv1.StatusInitializing {
			logger := r.GetHelperLogger(redisCluster.NamespacedName())
			robin, err := kubernetes.GetRobin(ctx, r.Client, redisCluster, logger)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Error getting Robin to update the status")
				return err
			}

			err = robin.SetStatus(redisCluster.Status.Status)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Error setting the new status to Robin", "status", redisCluster.Status.Status)
				return err
			}
			r.LogInfo(redisCluster.NamespacedName(), "Robin has been notified of the new status", "status", redisCluster.Status.Status)
		}

		// Update Robin ConfigMap status
		err = kubernetes.PersistRobinStatut(ctx, r.Client, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error updating the new status in Robin ConfigMap", "status", redisCluster.Status.Status)
			return err
		}
		r.LogInfo(redisCluster.NamespacedName(), "Robin ConfigMap has been updated with the new status", "status", redisCluster.Status.Status)

		return nil
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

func (r *RedisClusterReconciler) checkAndCreateK8sObjects(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) (bool, error) {
	var immediateRequeue bool = false
	var err error = nil
	var configMap *corev1.ConfigMap

	// RedisCluster check
	err = r.CheckAndUpdateRDCL(ctx, req.Name, redisCluster)
	if err != nil {
		r.LogError(redisCluster.NamespacedName(), err, "Error checking RedisCluster object")
		return immediateRequeue, err
	}

	// ConfigMap check
	if configMap, immediateRequeue, err = r.CheckAndCreateConfigMap(ctx, req, redisCluster); err != nil {
		return immediateRequeue, err
	}

	// PodDisruptionBudget check
	r.checkAndManagePodDisruptionBudget(ctx, req, redisCluster)

	// StatefulSet check
	if immediateRequeue, err = r.CheckAndCreateStatefulSet(ctx, req, redisCluster, configMap); err != nil {
		return immediateRequeue, err
	}

	// Robin deployment check
	r.checkAndCreateRobin(ctx, req, redisCluster)

	// Service check
	immediateRequeue, err = r.checkAndCreateService(ctx, req, redisCluster)

	return immediateRequeue, err
}

func (r *RedisClusterReconciler) CheckAndCreateConfigMap(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) (*corev1.ConfigMap, bool, error) {
	var immediateRequeue = false
	var auth = &corev1.Secret{}
	var err error

	configMap, err := r.FindExistingConfigMapFunc(ctx, req)
	if err != nil {
		if errors.IsNotFound(err) {
			if len(redisCluster.Spec.Auth.SecretName) > 0 {
				auth, err = r.GetSecret(ctx, types.NamespacedName{
					Name:      redisCluster.Spec.Auth.SecretName,
					Namespace: req.Namespace,
				}, redisCluster.NamespacedName())
				if err != nil {
					r.LogError(redisCluster.NamespacedName(), err, "Can't find provided secret", "redisCluster", redisCluster)
					return nil, immediateRequeue, err
				}
			}
			configMap = r.CreateConfigMap(req, redisCluster.Spec, auth, redisCluster.GetObjectMeta().GetLabels())
			ctrl.SetControllerReference(redisCluster, configMap, r.Scheme)
			r.LogInfo(redisCluster.NamespacedName(), "Creating configmap", "configmap", configMap.Name)
			createMapErr := r.Client.Create(ctx, configMap)
			if createMapErr != nil {
				r.LogError(redisCluster.NamespacedName(), createMapErr, "Error when creating configmap")
				return nil, immediateRequeue, createMapErr
			}
		} else {
			r.LogError(redisCluster.NamespacedName(), err, "Getting configmap data failed")
			return nil, immediateRequeue, err
		}
	}
	return configMap, immediateRequeue, nil
}

func (r *RedisClusterReconciler) CheckAndCreateStatefulSet(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster, configMap *corev1.ConfigMap) (bool, error) {
	var immediateRequeue = false
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

		// When scaling up before upgrading we can have inconsistencies currReadyNodes <> currSsetReplicas
		// we skip these checks till the sacling up is done.
		if redisCluster.Status.Status == redisv1.StatusUpgrading && redisCluster.Status.Substatus.Status == redisv1.SubstatusScalingUp {
			return immediateRequeue, nil
		}

		currSsetReplicas := *(statefulSet.Spec.Replicas)
		realExpectedReplicas := int32(redisCluster.NodesNeeded())
		immediateRequeue = false
		var err error
		if redisCluster.Status.Status == "" || redisCluster.Status.Status == redisv1.StatusInitializing {
			if currSsetReplicas != realExpectedReplicas {
				immediateRequeue = true
				r.LogInfo(redisCluster.NamespacedName(), "Replicas updated before reaching Configuring status: aligning StatefulSet <-> RedisCluster replicas",
					"StatefulSet replicas", currSsetReplicas, "RedisCluster replicas", realExpectedReplicas)
				statefulSet.Spec.Replicas = &realExpectedReplicas
				_, err = r.UpdateStatefulSet(ctx, statefulSet, redisCluster)
				if err != nil {
					r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
				}
				return true, err
			}
		}
		if realExpectedReplicas < currSsetReplicas {
			// Inconsistency: if a scaleup could not be completed because of a lack of resources that prevented
			// all the needed pods from being created
			// StatefulSet replicas are then aligned with RedisCluster replicas
			r.LogInfo(redisCluster.NamespacedName(), "Not all required pods instantiated: aligning StatefulSet <-> RedisCluster replicas",
				"StatefulSet replicas", currSsetReplicas, "RedisCluster replicas", realExpectedReplicas)
			statefulSet.Spec.Replicas = &realExpectedReplicas
			_, err = r.UpdateStatefulSet(ctx, statefulSet, redisCluster)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet replicas")
			}
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "StatefulSet - Not all pods Ready", "StatefulSet replicas", currSsetReplicas,
				"RedisCluster replicas", realExpectedReplicas)
			immediateRequeue = true
		}
		return immediateRequeue, err
	}
	return immediateRequeue, nil
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

	r.scaleDownRobin(ctx, redisCluster)

	r.deletePodDisruptionBudget(ctx, redisCluster)

	// All conditions set to false. Status set to Ready.
	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, redisv1.StatusReady) {
		redisCluster.Status.Status = redisv1.StatusReady
		SetAllConditionsFalse(r.GetHelperLogger(redisCluster.NamespacedName()), redisCluster)
		update_err = r.updateClusterStatus(ctx, redisCluster)
	}

	return update_err
}
