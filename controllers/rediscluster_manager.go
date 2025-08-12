// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"slices"
	"strings"
	"time"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	finalizer "github.com/inditextech/redkeyoperator/internal/finalizers"
	"github.com/inditextech/redkeyoperator/internal/kubernetes"
	"github.com/inditextech/redkeyoperator/internal/robin"

	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"

	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	DefaultRequeueTimeout time.Duration = 5
	ScalingDefaultTimeout time.Duration = 30
	ReadyRequeueTimeout   time.Duration = 30
	ErrorRequeueTimeout   time.Duration = 30
)

func NewRedKeyClusterReconciler(mgr ctrl.Manager, maxConcurrentReconciles int, concurrentMigrates int) *RedKeyClusterReconciler {
	eventRecorder := mgr.GetEventRecorderFor("redkeycluster-controller")
	reconciler := &RedKeyClusterReconciler{
		Client:                  mgr.GetClient(),
		Log:                     ctrl.Log.WithName("controllers").WithName("RedKeyCluster"),
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

	reconciler.FindExistingStatefulSetFunc = reconciler.DoFindExistingStatefulSet
	reconciler.FindExistingConfigMapFunc = reconciler.DoFindExistingConfigMap
	reconciler.FindExistingDeploymentFunc = reconciler.DoFindExistingDeployment
	reconciler.FindExistingPodDisruptionBudgetFunc = reconciler.DoFindExistingPodDisruptionBudget

	return reconciler
}

func (r *RedKeyClusterReconciler) ReconcileClusterObject(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedKeyCluster) (ctrl.Result, error) {
	var err error
	var requeueAfter time.Duration = DefaultRequeueTimeout
	currentStatus := redkeyCluster.Status

	r.logInfo(redkeyCluster.NamespacedName(), "RedKeyCluster reconciler start", "status", redkeyCluster.Status.Status)

	// Checks the existance of the ConfigMap, StatefulSet, Pods, Robin Deployment, PDB and Service,
	// creating the objects not created yet.
	// Coherence and configuration details are also checked and fixed.
	immediateRequeue, err := r.checkAndCreateK8sObjects(ctx, req, redkeyCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if immediateRequeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, err
	}

	// Check storage configuration consistency, updating status if needed.
	r.checkStorageConfigConsistency(ctx, redkeyCluster, redkeyCluster.Status.Status != redkeyv1.StatusError)

	// Redis cluster scaled to 0 replicas?
	// If it's a newly deployed cluster it won't have a status set yet and won't be catched here.
	if redkeyCluster.Spec.Replicas == 0 && redkeyCluster.Status.Status != "" {
		err = r.clusterScaledToZeroReplicas(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error managing cluster scaled to 0 replicas")
		}

		// Requeue to recheck, reconciliation ends here!
		r.logInfo(redkeyCluster.NamespacedName(), "RedKeyCluster reconciler end", "status", redkeyCluster.Status.Status)
		return ctrl.Result{RequeueAfter: time.Second * ReadyRequeueTimeout}, err
	}

	immediateRequeue, err = r.checkFinalizers(ctx, redkeyCluster)
	if immediateRequeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, err
	}

	requeue := false
	switch redkeyCluster.Status.Status {
	case "":
		requeue, requeueAfter = r.reconcileStatusNew(redkeyCluster)
	case redkeyv1.StatusInitializing:
		requeue, requeueAfter = r.reconcileStatusInitializing(ctx, redkeyCluster)
	case redkeyv1.StatusConfiguring:
		requeue, requeueAfter = r.reconcileStatusConfiguring(ctx, redkeyCluster)
	case redkeyv1.StatusReady:
		requeue, requeueAfter = r.reconcileStatusReady(ctx, redkeyCluster)
	case redkeyv1.StatusUpgrading:
		requeue, requeueAfter = r.reconcileStatusUpgrading(ctx, redkeyCluster)
	case redkeyv1.StatusScalingDown:
		requeue, requeueAfter = r.reconcileStatusScalingDown(ctx, redkeyCluster)
	case redkeyv1.StatusScalingUp:
		requeue, requeueAfter = r.reconcileStatusScalingUp(ctx, redkeyCluster)
	case redkeyv1.StatusError:
		requeue, requeueAfter = r.reconcileStatusError(ctx, redkeyCluster)
	default:
		r.logError(redkeyCluster.NamespacedName(), nil, "Status not allowed", "status", redkeyCluster.Status.Status)
		return ctrl.Result{}, nil
	}

	if err = r.refreshClusterNodesInfo(ctx, redkeyCluster); err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error updating cluster nodes")
	}

	var updateErr error
	if !redkeyv1.IsFastOperationStatus(redkeyCluster.Status.Substatus) && !redkeyv1.CompareStatuses(&redkeyCluster.Status, &currentStatus) {
		updateErr = r.updateClusterStatus(ctx, redkeyCluster)
	}

	r.logInfo(redkeyCluster.NamespacedName(), "RedKeyCluster reconciler end", "status", redkeyCluster.Status.Status)

	if requeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, updateErr
	}
	return ctrl.Result{}, updateErr
}

func (r *RedKeyClusterReconciler) reconcileStatusNew(redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	r.logInfo(redkeyCluster.NamespacedName(), "New RedKeyCluster. Initializing...")
	if redkeyCluster.Status.Nodes == nil {
		redkeyCluster.Status.Nodes = make(map[string]*redkeyv1.RedisNode, 0)
	}
	redkeyCluster.Status.Status = redkeyv1.StatusInitializing
	return requeue, DefaultRequeueTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusInitializing(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {

	// Check Redis node pods rediness
	nodePodsReady, err := r.allPodsReady(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Could not check for Redis node pods being ready")
		return true, DefaultRequeueTimeout
	}
	if !nodePodsReady {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis node pods to become ready")
		return true, DefaultRequeueTimeout
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Redis node pods are ready")

	// Check Robin pod readiness
	logger := r.getHelperLogger(redkeyCluster.NamespacedName())
	robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check its readiness")
		return true, DefaultRequeueTimeout
	}
	flag, err := kubernetes.PodRunningReady(robin.Pod)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking Robin pod readiness")
		return true, DefaultRequeueTimeout
	}
	if !flag {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin pod to become ready")
		return true, DefaultRequeueTimeout
	}

	// Check Robin is responding to requests
	status, err := robin.GetStatus()
	if err != nil {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Robin accepting requests")
		return true, DefaultRequeueTimeout
	}
	r.logInfo(redkeyCluster.NamespacedName(), "Status", "status", status)

	// Redis pods and Robin are ok, moving to Configuring status
	redkeyCluster.Status.Status = redkeyv1.StatusConfiguring

	return false, DefaultRequeueTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusConfiguring(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true

	// Ask Robin for Redis cluster readiness
	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	robinRedis, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to check the cluster readiness")
		return true, DefaultRequeueTimeout
	}
	replicas, replicasPerMaster, err := robinRedis.GetReplicas()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin replicas")
		return true, DefaultRequeueTimeout
	}

	// Update Robin replicas if needed.
	if replicas != int(redkeyCluster.Spec.Replicas) || replicasPerMaster != int(redkeyCluster.Spec.ReplicasPerMaster) {
		r.logInfo(redkeyCluster.NamespacedName(), "Robin replicas updated", "replicas before", replicas, "replicas after", redkeyCluster.Spec.Replicas,
			"replicas per master before", replicasPerMaster, "replicas per master after", redkeyCluster.Spec.ReplicasPerMaster)
		err = robinRedis.SetReplicas(int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error updating Robin replicas")
			return true, DefaultRequeueTimeout
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redkeyCluster, int(redkeyCluster.Spec.Replicas), int(redkeyCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, DefaultRequeueTimeout
		}
		return true, DefaultRequeueTimeout // Requeue to let Robin update the cluster
	}

	// Check cluster readiness.
	check, errors, warnings, err := robinRedis.ClusterCheck()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking the cluster readiness over Robin")
		return true, DefaultRequeueTimeout
	}

	if !check {
		r.logInfo(redkeyCluster.NamespacedName(), "Waiting for Redis cluster readiness", "errors", errors, "warnings", warnings)
		return true, DefaultRequeueTimeout
	}

	// Redis cluster is ok, moving to Ready status
	redkeyCluster.Status.Status = redkeyv1.StatusReady

	return requeue, DefaultRequeueTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusReady(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter time.Duration = ReadyRequeueTimeout

	// Reconcile PDB
	err := r.checkAndUpdatePodDisruptionBudget(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error checking PDB changes")
	}

	// Check and update RedKeyCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redkeyCluster.Status.Status == redkeyv1.StatusReady {
		err = r.updateScalingStatus(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error when updating scaling status")
		}
	}
	// -> Cluster needs to be upgraded?
	if redkeyCluster.Status.Status == redkeyv1.StatusReady {
		err = r.updateUpgradingStatus(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error when updating upgrading status")
		}
	}

	// Requeue to check periodically the cluster is well formed and fix it if needed
	return requeue, requeueAfter
}

func (r *RedKeyClusterReconciler) reconcileStatusUpgrading(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true

	err := r.upgradeCluster(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when upgrading cluster")
		r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
	}

	return requeue, DefaultRequeueTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusScalingDown(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	immediateRequeue, err := r.scaleCluster(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when scaling down")
		r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
		redkeyCluster.Status.Status = redkeyv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	if immediateRequeue {
		// Scaling the cluster may require requeues to wait for operations being done
		return true, ScalingDefaultTimeout
	}
	err = r.updateScalingStatus(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, ScalingDefaultTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusScalingUp(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	immediateRequeue, err := r.scaleCluster(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when scaling up")
		r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
		redkeyCluster.Status.Status = redkeyv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	if immediateRequeue {
		// Scaling the cluster may require requeues to wait for operations being done
		return true, ScalingDefaultTimeout
	}
	err = r.updateScalingStatus(ctx, redkeyCluster)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, ScalingDefaultTimeout
}

func (r *RedKeyClusterReconciler) reconcileStatusError(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter = ErrorRequeueTimeout

	// If storage config is not consistent do not try to recover from Error.
	if !r.checkStorageConfigConsistency(ctx, redkeyCluster, false) {
		return requeue, requeueAfter
	}

	// Check and update RedKeyCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redkeyCluster.Status.Status == redkeyv1.StatusError {
		err := r.updateScalingStatus(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error when updating scaling status")
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
		}
	}
	// -> Cluster needs to be upgraded?
	if redkeyCluster.Status.Status == redkeyv1.StatusError {
		err := r.updateUpgradingStatus(ctx, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error when updating upgrading status")
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
		}
	}

	return requeue, requeueAfter
}

func (r *RedKeyClusterReconciler) refreshClusterNodesInfo(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) error {

	// Redis cluster must be in Configuring status (or greater) to be able to query Robin for nodes.
	if redkeyCluster.Status.Status == "" || redkeyCluster.Status.Status == redkeyv1.StatusInitializing {
		return nil
	}

	logger := r.getHelperLogger((redkeyCluster.NamespacedName()))
	robin, err := robin.NewRobin(ctx, r.Client, redkeyCluster, logger)
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting Robin to get cluster nodes")
		return err
	}
	clusterNodes, err := robin.GetClusterNodes()
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Error getting cluster nodes from Robin")
		return err
	}

	updatedNodes := make(map[string]*redkeyv1.RedisNode, 0)
	for _, node := range clusterNodes.Nodes {
		updatedNodes[node.Id] = &redkeyv1.RedisNode{IP: node.Ip, Name: node.Name, ReplicaOf: node.MasterId}
		updatedNodes[node.Id].IsMaster = strings.Contains(node.Flags, "master")
	}

	redkeyCluster.Status.Nodes = updatedNodes

	return nil
}

// Checks storage configuration for inconsistencies.
// If the parameter is set to true if a check fail the function returns issuing log info, generating an event and setting the Redis cluster Status to Error.
// Returns true if all checks pass or false if any checks fail.
func (r *RedKeyClusterReconciler) checkStorageConfigConsistency(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster, updateRDCL bool) bool {
	var stsStorage, stsStorageClassName string

	sts, err := r.FindExistingStatefulSet(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name, Namespace: redkeyCluster.Namespace}})
	if err != nil {
		r.logError(redkeyCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}
	if sts == nil {
		err = errors.NewNotFound(schema.GroupResource{Group: "", Resource: "Statefulset"}, "StatefulSet not found")
		r.logError(redkeyCluster.NamespacedName(), err, "Cannot find existing statefulset, maybe it was deleted.")
		return false
	}

	// Get configured Storage and StorageClassName from StatefulSet
	if len(sts.Spec.VolumeClaimTemplates) > 0 {
		stsStorage = sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()
		r.logInfo(redkeyCluster.NamespacedName(), "Current StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage", stsStorage)
		if sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName != nil {
			stsStorageClassName = *sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName
			r.logInfo(redkeyCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", stsStorageClassName)
		} else {
			r.logInfo(redkeyCluster.NamespacedName(), "Currect StatefulSet Storage configuration", "Spec.VolumeClaimTemplates[0].Spec.StorageClassName", "Not set")
		}
	}

	// Non ephemeral cluster checks:
	// - Updates to redkeyCluster.Spec.Storage are not allowed
	if !redkeyCluster.Spec.Ephemeral && redkeyCluster.Spec.Storage != "" && stsStorage != "" && redkeyCluster.Spec.Storage != stsStorage {
		err = errors.NewBadRequest("spec: Forbidden: updates to redkeyCluster.Spec.Storage field are not allowed")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redkeyCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// - Unset redkeyCluster.Spec.Storage is not allowed
	if !redkeyCluster.Spec.Ephemeral && redkeyCluster.Spec.Storage == "" && stsStorage != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redkeyCluster.Spec.Storage field are not allowed")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storage", stsStorage, "RDCL storage", redkeyCluster.Spec.Storage)
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// - Updates to redkeyCluster.Spec.StorageClassName are not allowed
	if !redkeyCluster.Spec.Ephemeral && redkeyCluster.Spec.StorageClassName != "" && stsStorageClassName != "" && redkeyCluster.Spec.StorageClassName != stsStorageClassName {
		err = errors.NewBadRequest("spec: Forbidden: updates to redkeyCluster.Spec.StorageClassName field are not allowed")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redkeyCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// - Set redkeyCluster.Spec.StorageClassName is not allowed
	if !redkeyCluster.Spec.Ephemeral && redkeyCluster.Spec.StorageClassName != "" && stsStorageClassName == "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redkeyCluster.Spec.StorageClassName field are not allowed")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS storageClassName", stsStorageClassName, "RDCL storageClassName", redkeyCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// - Unset redkeyClusterSpec.StorageClassName is not allowed
	if !redkeyCluster.Spec.Ephemeral && redkeyCluster.Spec.StorageClassName == "" && stsStorageClassName != "" {
		err = errors.NewBadRequest("spec: Forbidden: updates to redkeyCluster.Spec.StorageClassName field are not allowed")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster storage configuration updates are forbidden", "STS stsStorageClassName", stsStorageClassName, "RDCL stsStorageClassName", redkeyCluster.Spec.StorageClassName)
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// - Moving from ephemeral to non ephemeral is not allowed
	if !redkeyCluster.Spec.Ephemeral && len(sts.Spec.VolumeClaimTemplates) == 0 {
		err = errors.NewBadRequest("spec: Error: non ephemeral cluster without VolumeClaimTemplates defined in the StatefulSet")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from ephemeral to non ephemeral)")
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}
	// Ephemeral cluster checks:
	// - Moving from non ephemeral to ephemeral is not allowed
	if redkeyCluster.Spec.Ephemeral && len(sts.Spec.VolumeClaimTemplates) > 0 {
		err = errors.NewBadRequest("spec: Error: ephemeral cluster with VolumeClaimTemplates defined in the StatefulSet")
		r.logError(redkeyCluster.NamespacedName(), err, "Redis cluster misconfigured (probably trying to change from non ephemeral to ephemeral)")
		if updateRDCL {
			r.Recorder.Event(redkeyCluster, "Warning", "ClusterError", err.Error())
			redkeyCluster.Status.Status = redkeyv1.StatusError
		}
		return false
	}

	return true
}

func (r *RedKeyClusterReconciler) checkFinalizers(ctx context.Context, redkeyCluster *redkeyv1.RedKeyCluster) (bool, error) {
	var pvcFinalizer = (&finalizer.DeletePVCFinalizer{}).GetId()
	if redkeyCluster.Spec.DeletePVC && !redkeyCluster.Spec.Ephemeral {
		r.logInfo(redkeyCluster.NamespacedName(), "Delete PVCs feature enabled in cluster spec")
		if !controllerutil.ContainsFinalizer(redkeyCluster, pvcFinalizer) {
			controllerutil.AddFinalizer(redkeyCluster, pvcFinalizer)
			r.Update(ctx, redkeyCluster)
			r.logInfo(redkeyCluster.NamespacedName(), "Added finalizer. Deleting PVCs after scale down or cluster deletion")
		}
	} else {
		r.logInfo(redkeyCluster.NamespacedName(), "Delete PVCs feature disabled in cluster spec or not specified. PVCs won't be deleted after scaling down or cluster deletion")
	}

	if !redkeyCluster.GetDeletionTimestamp().IsZero() {
		for _, f := range r.Finalizers {
			if slices.Contains(redkeyCluster.GetFinalizers(), f.GetId()) {
				r.logInfo(redkeyCluster.NamespacedName(), "Running finalizer", "id", f.GetId(), "finalizer", f)
				finalizerError := f.DeleteMethod(ctx, redkeyCluster, r.Client)
				if finalizerError != nil {
					r.logError(redkeyCluster.NamespacedName(), finalizerError, "Finalizer returned error", "id", f.GetId(), "finalizer", f)
				}
				controllerutil.RemoveFinalizer(redkeyCluster, f.GetId())
				if err := r.Update(ctx, redkeyCluster); err != nil {
					return true, err
				}
			}
		}
	}

	return false, nil
}

func (r *RedKeyClusterReconciler) FindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return r.FindExistingStatefulSetFunc(ctx, req)
}

func (r *RedKeyClusterReconciler) DoFindExistingStatefulSet(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error) {
	return kubernetes.FindExistingStatefulSet(ctx, r.Client, req)
}

func (r *RedKeyClusterReconciler) FindExistingConfigMap(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return r.FindExistingConfigMapFunc(ctx, req)
}

func (r *RedKeyClusterReconciler) DoFindExistingConfigMap(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error) {
	return kubernetes.FindExistingConfigMap(ctx, r.Client, req)
}

func (r *RedKeyClusterReconciler) FindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return r.FindExistingDeploymentFunc(ctx, req)
}

func (r *RedKeyClusterReconciler) DoFindExistingDeployment(ctx context.Context, req ctrl.Request) (*v1.Deployment, error) {
	return kubernetes.FindExistingDeployment(ctx, r.Client, req)
}

func (r *RedKeyClusterReconciler) FindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return r.FindExistingPodDisruptionBudgetFunc(ctx, req)
}

func (r *RedKeyClusterReconciler) DoFindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return kubernetes.FindExistingPodDisruptionBudget(ctx, r.Client, req)
}

func (r *RedKeyClusterReconciler) FindExistingService(ctx context.Context, req ctrl.Request) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, svc)
	if err != nil {
		return nil, err
	}
	return svc, nil
}
