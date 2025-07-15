// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	finalizer "github.com/inditextech/redisoperator/internal/finalizers"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/robin"
	"github.com/inditextech/redisoperator/internal/utils"

	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	DefaultRequeueTimeout time.Duration = 5
	ReadyRequeueTimeout   time.Duration = 30
	ErrorRequeueTimeout   time.Duration = 30
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
	var requeueAfter time.Duration = DefaultRequeueTimeout
	currentStatus := redisCluster.Status

	r.logInfo(redisCluster.NamespacedName(), "RedisCluster reconciler start", "status", redisCluster.Status.Status)

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
			r.logError(redisCluster.NamespacedName(), err, "Error managing cluster scaled to 0 replicas")
		}

		// Requeue to recheck, reconciliation ends here!
		r.logInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)
		return ctrl.Result{RequeueAfter: time.Second * ReadyRequeueTimeout}, err
	}

	if redisCluster.Spec.DeletePVC && !redisCluster.Spec.Ephemeral {
		r.logInfo(redisCluster.NamespacedName(), "Delete PVCs feature enabled in cluster spec")
		if !controllerutil.ContainsFinalizer(redisCluster, pvcFinalizer) {
			controllerutil.AddFinalizer(redisCluster, pvcFinalizer)
			r.Update(ctx, redisCluster)
			r.logInfo(redisCluster.NamespacedName(), "Added finalizer. Deleting PVCs after scale down or cluster deletion")
		}
	} else {
		r.logInfo(redisCluster.NamespacedName(), "Delete PVCs feature disabled in cluster spec or not specified. PVCs won't be deleted after scaling down or cluster deletion")
	}

	if !redisCluster.GetDeletionTimestamp().IsZero() {
		for _, f := range r.Finalizers {
			if slices.Contains(redisCluster.GetFinalizers(), f.GetId()) {
				r.logInfo(redisCluster.NamespacedName(), "Running finalizer", "id", f.GetId(), "finalizer", f)
				finalizerError := f.DeleteMethod(ctx, redisCluster, r.Client)
				if finalizerError != nil {
					r.logError(redisCluster.NamespacedName(), finalizerError, "Finalizer returned error", "id", f.GetId(), "finalizer", f)
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
		r.logError(redisCluster.NamespacedName(), nil, "Status not allowed", "status", redisCluster.Status.Status)
		return ctrl.Result{}, nil
	}

	if err = r.updateClusterNodes(ctx, redisCluster); err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating cluster nodes")
	}

	var updateErr error
	if !reflect.DeepEqual(redisCluster.Status, currentStatus) {
		updateErr = r.updateClusterStatus(ctx, redisCluster)
	}

	r.logInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)

	if requeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, updateErr
	}
	return ctrl.Result{}, updateErr
}

// Checks storage configuration for inconsistencies.
// If the parameter is set to true if a check fail the function returns issuing log info, generating an event and setting the Redis cluster Status to Error.
// Returns true if all checks pass or false if any checks fail.
func (r *RedisClusterReconciler) checkStorageConfigConsistency(ctx context.Context, redisCluster *redisv1.RedisCluster, updateRDCL bool) bool {
	var stsStorage, stsStorageClassName string

	sts, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}
	if sts == nil {
		err = errors.NewNotFound(schema.GroupResource{Group: "", Resource: "Statefulset"}, "StatefulSet not found")
		r.logError(redisCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}

	// Get configured Storage and StorageClassName from StatefulSet
	if len(sts.Spec.VolumeClaimTemplates) > 0 {
		stsStorage = sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()
		r.logInfo(redisCluster.NamespacedName(), "Current StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage", stsStorage)
		if sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName != nil {
			stsStorageClassName = *sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName
			r.logInfo(redisCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", stsStorageClassName)
		} else {
			r.logInfo(redisCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", "Not set")
		}
	}

	// Non ephemeral cluster checks:
	// - Updates to redisCluster.Spec.Storage are not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.Storage != "" && stsStorage != "" && redisCluster.Spec.Storage != stsStorage {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.Storage field are not allowed")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redisCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Unset redisCluster.Spec.Storage is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.Storage == "" && stsStorage != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.Storage field are not allowed")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redisCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Updates to redisCluster.Spec.StorageClassName are not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName != "" && stsStorageClassName != "" && redisCluster.Spec.StorageClassName != stsStorageClassName {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Set redisCluster.Spec.StorageClassName is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName != "" && stsStorageClassName == "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Unset redisClusterSpec.StorageClassName is not allowed
	if !redisCluster.Spec.Ephemeral && redisCluster.Spec.StorageClassName == "" && stsStorageClassName != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redisCluster.Spec.StorageClassName field are not allowed")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS stsStorageClassName", stsStorageClassName, "RDCL stsStorageClassName", redisCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			redisCluster.Status.Status = redisv1.StatusError
		}
		return false
	}
	// - Moving from ephemeral to non ephemeral is not allowed
	if !redisCluster.Spec.Ephemeral && len(sts.Spec.VolumeClaimTemplates) == 0 {
		err = errors.NewBadRequest("spec: Error: non ephemeral cluster without VolumeClaimTemplates defined in the StatefulSet")
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from ephemeral to non ephemeral)")
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
		r.logError(redisCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from non ephemeral to ephemeral)")
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
	r.logInfo(redisCluster.NamespacedName(), "New RedisCluster. Initializing...")
	if redisCluster.Status.Nodes == nil {
		redisCluster.Status.Nodes = make(map[string]*redisv1.RedisNode, 0)
	}
	redisCluster.Status.Status = redisv1.StatusInitializing
	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusInitializing(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {

	// Check Redis nod pods rediness
	nodePodsReady, err := r.allPodsReady(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return true, DefaultRequeueTimeout
	}
	if !nodePodsReady {
		r.logInfo(redisCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return true, DefaultRequeueTimeout
	}
	r.logInfo(redisCluster.NamespacedName(), "Redis node pods are ready")

	// Check Robin pod readiness
	logger := r.getHelperLogger(redisCluster.NamespacedName())
	robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return true, DefaultRequeueTimeout
	}
	flag, err := utils.PodRunningReady(robin.Pod)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error checking Robin pod readiness")
		return true, DefaultRequeueTimeout
	}
	if !flag {
		r.logInfo(redisCluster.NamespacedName(), "Waiting for Robin pod to become ready")
		return true, DefaultRequeueTimeout
	}

	// Check Robin is responding to requests
	status, err := robin.GetStatus()
	if err != nil {
		r.logInfo(redisCluster.NamespacedName(), "Waiting for Robin accepting requests")
		return true, DefaultRequeueTimeout
	}
	r.logInfo(redisCluster.NamespacedName(), "Status", "status", status)

	// Redis pods and Robin are ok, moving to Configuring status
	redisCluster.Status.Status = redisv1.StatusConfiguring

	return false, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusConfiguring(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true

	// Ask Robin for Redis cluster readiness
	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to check the cluster readiness")
		return true, DefaultRequeueTimeout
	}
	check, errors, warnings, err := robin.ClusterCheck()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return true, DefaultRequeueTimeout
	}

	if !check {
		r.logInfo(redisCluster.NamespacedName(), "Waiting for Redis cluster readiness", "errors", errors, "warnings", warnings)
		return true, DefaultRequeueTimeout
	}

	// Redis cluster is ok, moving to Ready status
	redisCluster.Status.Status = redisv1.StatusReady

	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusReady(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter time.Duration = ReadyRequeueTimeout

	// Reconcile PDB
	err := r.checkAndUpdatePodDisruptionBudget(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error checking PDB changes")
	}

	// Check and update RedisCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.UpdateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.UpdateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
		}
	}

	// Requeue to check periodically the cluster is well formed and fix it if needed
	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) reconcileStatusUpgrading(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true

	err := r.UpgradeCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when upgrading cluster")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}

	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusScalingDown(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	err := r.ScaleCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when scaling down")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	err = r.UpdateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusScalingUp(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	err := r.ScaleCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when scaling up")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	err = r.UpdateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusError(ctx context.Context, redisCluster *redisv1.RedisCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter = ErrorRequeueTimeout
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
				r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
				r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
			}
		} else {
			r.logError(redisCluster.NamespacedName(), err, "Error when scaling cluster")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
		requeueAfter = DefaultRequeueTimeout
	}

	// Check and update RedisCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redisCluster.Status.Status == redisv1.StatusError {
		err = r.UpdateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusError {
		err = r.UpdateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}

	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) updateClusterNodes(ctx context.Context, redisCluster *redisv1.RedisCluster) error {

	// Redis cluster must be in Configuring status (or greater) to be able to query Robin for nodes.
	if redisCluster.Status.Status == "" || redisCluster.Status.Status == redisv1.StatusInitializing {
		return nil
	}

	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return err
	}
	clusterNodes, err := robin.GetClusterNodes()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}

	updatedNodes := make(map[string]*redisv1.RedisNode, 0)
	for _, node := range clusterNodes.Nodes {
		updatedNodes[node.Id] = &redisv1.RedisNode{IP: node.Ip, Name: node.Name, ReplicaOf: node.MasterId}
		updatedNodes[node.Id].IsMaster = strings.Contains(node.Flags, "master")
	}

	redisCluster.Status.Nodes = updatedNodes

	return nil
}

func (r *RedisClusterReconciler) updateClusterStatus(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	var req reconcile.Request
	req.NamespacedName.Namespace = redisCluster.Namespace
	req.NamespacedName.Name = redisCluster.Name

	r.logInfo(redisCluster.NamespacedName(), "New cluster status", "status", redisCluster.Status.Status)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		time.Sleep(time.Second * 1)

		// Update RedisCluster status first

		// get a fresh rediscluster to minimize conflicts
		refreshedRedisCluster := redisv1.RedisCluster{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		// update the slots
		refreshedRedisCluster.Status.Nodes = redisCluster.Status.Nodes
		refreshedRedisCluster.Status.Status = redisCluster.Status.Status
		refreshedRedisCluster.Status.Conditions = redisCluster.Status.Conditions
		refreshedRedisCluster.Status.Substatus = redisCluster.Status.Substatus

		err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating RedisCluster object with new status")
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "RedisCluster has been updated with the new status", "status", redisCluster.Status.Status)

		// Update Robin status
		// Do not update if we are switching to Initializing status because Robin needs some
		// time to be ready to accept API requests.
		if redisCluster.Status.Status != redisv1.StatusInitializing {
			logger := r.getHelperLogger(redisCluster.NamespacedName())
			robin, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to update the status")
				return err
			}

			err = robin.SetStatus(redisCluster.Status.Status)
			if err != nil {
				r.logError(redisCluster.NamespacedName(), err, "Error setting the new status to Robin", "status", redisCluster.Status.Status)
				return err
			}
			r.logInfo(redisCluster.NamespacedName(), "Robin has been notified of the new status", "status", redisCluster.Status.Status)
		}

		// Update Robin ConfigMap status
		err = robin.PersistRobinStatut(ctx, r.Client, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating the new status in Robin ConfigMap", "status", redisCluster.Status.Status)
			return err
		}
		r.logInfo(redisCluster.NamespacedName(), "Robin ConfigMap has been updated with the new status", "status", redisCluster.Status.Status)

		return nil
	})
}

func (r *RedisClusterReconciler) updateClusterSubStatus(ctx context.Context, redisCluster *redisv1.RedisCluster, substatus string, partition int) error {
	refreshedRedisCluster := redisv1.RedisCluster{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name}, &refreshedRedisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
		return err
	}
	refreshedRedisCluster.Status.Substatus.Status = substatus
	refreshedRedisCluster.Status.Substatus.UpgradingPartition = partition
	redisCluster.Status.Substatus.Status = substatus
	redisCluster.Status.Substatus.UpgradingPartition = partition

	err = r.Client.Status().Update(ctx, &refreshedRedisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating substatus")
		return err
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
	r.logInfo(redisCluster.NamespacedName(), "Cluster spec replicas is set to 0", "SpecReplicas", redisCluster.Spec.Replicas)
	sset, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name, Namespace: redisCluster.Namespace}})
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Cannot find exists statefulset maybe is deleted.")
	}
	if sset != nil {
		if *(sset.Spec.Replicas) != 0 {
			r.logInfo(redisCluster.NamespacedName(), "Cluster scaled to 0 replicas")
			r.Recorder.Event(redisCluster, "Normal", "RedisClusterScaledToZero", fmt.Sprintf("Scaling down from %d to 0", *(sset.Spec.Replicas)))
		}
		*sset.Spec.Replicas = 0
		sset, err = r.UpdateStatefulSet(ctx, sset, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Failed to update StatefulSet")
		}
		r.logInfo(redisCluster.NamespacedName(), "StatefulSet updated", "Replicas", sset.Spec.Replicas)
	}

	r.scaleDownRobin(ctx, redisCluster)

	r.deletePodDisruptionBudget(ctx, redisCluster)

	// All conditions set to false. Status set to Ready.
	var update_err error
	if !reflect.DeepEqual(redisCluster.Status, redisv1.StatusReady) {
		redisCluster.Status.Status = redisv1.StatusReady
		setAllConditionsFalse(r.getHelperLogger(redisCluster.NamespacedName()), redisCluster)
		update_err = r.updateClusterStatus(ctx, redisCluster)
	}

	return update_err
}

func (r *RedisClusterReconciler) FindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return r.FindExistingStatefulSetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return kubernetes.FindExistingStatefulSet(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingConfigMap(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return r.FindExistingConfigMapFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingConfigMap(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return kubernetes.FindExistingConfigMap(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return r.FindExistingDeploymentFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return kubernetes.FindExistingDeployment(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return r.FindExistingPodDisruptionBudgetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return kubernetes.FindExistingPodDisruptionBudget(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) FindExistingService(ctx context.Context, req ctrl.Request) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, svc)
	if err != nil {
		return nil, err
	}
	return svc, nil
}
