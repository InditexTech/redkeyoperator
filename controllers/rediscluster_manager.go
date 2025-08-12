// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"slices"
	"strings"
	"time"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	finalizer "github.com/inditextech/redisoperator/internal/finalizers"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	"github.com/inditextech/redisoperator/internal/robin"

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

	reconciler.FindExistingStatefulSetFunc = reconciler.DoFindExistingStatefulSet
	reconciler.FindExistingConfigMapFunc = reconciler.DoFindExistingConfigMap
	reconciler.FindExistingDeploymentFunc = reconciler.DoFindExistingDeployment
	reconciler.FindExistingPodDisruptionBudgetFunc = reconciler.DoFindExistingPodDisruptionBudget

	return reconciler
}

func (r *RedisClusterReconciler) ReconcileClusterObject(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedKeyCluster) (ctrl.Result, error) {
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

	immediateRequeue, err = r.checkFinalizers(ctx, redisCluster)
	if immediateRequeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, err
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

	if err = r.refreshClusterNodesInfo(ctx, redisCluster); err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error updating cluster nodes")
	}

	var updateErr error
	if !redisv1.IsFastOperationStatus(redisCluster.Status.Substatus) && !redisv1.CompareStatuses(&redisCluster.Status, &currentStatus) {
		updateErr = r.updateClusterStatus(ctx, redisCluster)
	}

	r.logInfo(redisCluster.NamespacedName(), "RedisCluster reconciler end", "status", redisCluster.Status.Status)

	if requeue {
		return ctrl.Result{RequeueAfter: time.Second * requeueAfter}, updateErr
	}
	return ctrl.Result{}, updateErr
}

func (r *RedisClusterReconciler) reconcileStatusNew(redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	r.logInfo(redisCluster.NamespacedName(), "New RedisCluster. Initializing...")
	if redisCluster.Status.Nodes == nil {
		redisCluster.Status.Nodes = make(map[string]*redisv1.RedisNode, 0)
	}
	redisCluster.Status.Status = redisv1.StatusInitializing
	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusInitializing(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {

	// Check Redis node pods rediness
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
	flag, err := kubernetes.PodRunningReady(robin.Pod)
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

func (r *RedisClusterReconciler) reconcileStatusConfiguring(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true

	// Ask Robin for Redis cluster readiness
	logger := r.getHelperLogger((redisCluster.NamespacedName()))
	robinRedis, err := robin.NewRobin(ctx, r.Client, redisCluster, logger)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin to check the cluster readiness")
		return true, DefaultRequeueTimeout
	}
	replicas, replicasPerMaster, err := robinRedis.GetReplicas()
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error getting Robin replicas")
		return true, DefaultRequeueTimeout
	}

	// Update Robin replicas if needed.
	if replicas != int(redisCluster.Spec.Replicas) || replicasPerMaster != int(redisCluster.Spec.ReplicasPerMaster) {
		r.logInfo(redisCluster.NamespacedName(), "Robin replicas updated", "replicas before", replicas, "replicas after", redisCluster.Spec.Replicas,
			"replicas per master before", replicasPerMaster, "replicas per master after", redisCluster.Spec.ReplicasPerMaster)
		err = robinRedis.SetReplicas(int(redisCluster.Spec.Replicas), int(redisCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error updating Robin replicas")
			return true, DefaultRequeueTimeout
		}
		err = robin.PersistRobinReplicas(ctx, r.Client, redisCluster, int(redisCluster.Spec.Replicas), int(redisCluster.Spec.ReplicasPerMaster))
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error persisting Robin replicas")
			return true, DefaultRequeueTimeout
		}
		return true, DefaultRequeueTimeout // Requeue to let Robin update the cluster
	}

	// Check cluster readiness.
	check, errors, warnings, err := robinRedis.ClusterCheck()
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

func (r *RedisClusterReconciler) reconcileStatusReady(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
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
		err = r.updateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusReady {
		err = r.updateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
		}
	}

	// Requeue to check periodically the cluster is well formed and fix it if needed
	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) reconcileStatusUpgrading(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true

	err := r.upgradeCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when upgrading cluster")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}

	return requeue, DefaultRequeueTimeout
}

func (r *RedisClusterReconciler) reconcileStatusScalingDown(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	immediateRequeue, err := r.scaleCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when scaling down")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	if immediateRequeue {
		// Scaling the cluster may require requeues to wait for operations being done
		return true, ScalingDefaultTimeout
	}
	err = r.updateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, ScalingDefaultTimeout
}

func (r *RedisClusterReconciler) reconcileStatusScalingUp(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	immediateRequeue, err := r.scaleCluster(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when scaling up")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		redisCluster.Status.Status = redisv1.StatusError
		return requeue, DefaultRequeueTimeout
	}
	if immediateRequeue {
		// Scaling the cluster may require requeues to wait for operations being done
		return true, ScalingDefaultTimeout
	}
	err = r.updateScalingStatus(ctx, redisCluster)
	if err != nil {
		r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
		r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
	}
	return requeue, ScalingDefaultTimeout
}

func (r *RedisClusterReconciler) reconcileStatusError(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, time.Duration) {
	var requeue = true
	var requeueAfter = ErrorRequeueTimeout

	// If storage config is not consistent do not try to recover from Error.
	if !r.checkStorageConfigConsistency(ctx, redisCluster, false) {
		return requeue, requeueAfter
	}

	// Check and update RedisCluster status value accordingly to its configuration and status
	// -> Cluster needs to be scaled?
	if redisCluster.Status.Status == redisv1.StatusError {
		err := r.updateScalingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating scaling status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}
	// -> Cluster needs to be upgraded?
	if redisCluster.Status.Status == redisv1.StatusError {
		err := r.updateUpgradingStatus(ctx, redisCluster)
		if err != nil {
			r.logError(redisCluster.NamespacedName(), err, "Error when updating upgrading status")
			r.Recorder.Event(redisCluster, "Warning", "ClusterError", err.Error())
		}
	}

	return requeue, requeueAfter
}

func (r *RedisClusterReconciler) refreshClusterNodesInfo(ctx context.Context, redisCluster *redisv1.RedKeyCluster) error {

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

// Checks storage configuration for inconsistencies.
// If the parameter is set to true if a check fail the function returns issuing log info, generating an event and setting the Redis cluster Status to Error.
// Returns true if all checks pass or false if any checks fail.
func (r *RedisClusterReconciler) checkStorageConfigConsistency(ctx context.Context, redisCluster *redisv1.RedKeyCluster, updateRDCL bool) bool {
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

func (r *RedisClusterReconciler) checkFinalizers(ctx context.Context, redisCluster *redisv1.RedKeyCluster) (bool, error) {
	var pvcFinalizer = (&finalizer.DeletePVCFinalizer{}).GetId()
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
					return true, err
				}
			}
		}
	}

	return false, nil
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
