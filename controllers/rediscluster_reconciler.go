// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/client-go/tools/record"

	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	finalizer "github.com/inditextech/redisoperator/internal/finalizers"
)

// RedisClusterReconciler reconciles a RedisCluster object
type RedisClusterReconciler struct {
	client.Client
	Log                                 logr.Logger
	Scheme                              *runtime.Scheme
	Recorder                            record.EventRecorder
	Finalizers                          []finalizer.Finalizer
	MaxConcurrentReconciles             int
	ConcurrentMigrate                   int
	GetClusterInfoFunc                  func(ctx context.Context, redisCluster *redisv1.RedisCluster) map[string]string
	GetReadyNodesFunc                   func(ctx context.Context, redisCluster *redisv1.RedisCluster) (map[string]*redisv1.RedisNode, error)
	FindExistingStatefulSetFunc         func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error)
	FindExistingConfigMapFunc           func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error)
	FindExistingDeploymentFunc          func(ctx context.Context, req ctrl.Request) (*v1.Deployment, error)
	FindExistingPodDisruptionBudgetFunc func(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error)
}

// +kubebuilder:rbac:groups=redis.inditex.com,resources=redisclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redis.inditex.com,resources=redisclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redis.inditex.com,resources=redisclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmap;services;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;create;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=create;delete;patch;update
func (r *RedisClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("RedisCluster reconciler called", "redis-cluster", req.NamespacedName, "name", req.Name, "ns", req.Namespace)
	redisCluster := &redisv1.RedisCluster{}
	err := r.Client.Get(ctx, req.NamespacedName, redisCluster)
	r.RefreshRedisClients(ctx, redisCluster)
	if err == nil {
		r.Log.Info("Found RedisCluster", "redis-cluster", req.NamespacedName, "name", redisCluster.GetName(), "GVK", redisCluster.GroupVersionKind().String(), "status", redisCluster.Status.Status)
		return r.ReconcileClusterObject(ctx, req, redisCluster)
	} else {
		// cluster deleted
		r.Log.Info("Can't find RedisCluster, probably deleted", "redis-cluster", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1.RedisCluster{}).
		Watches(&corev1.ConfigMap{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(r.PreFilter())).
		Owns(&v1.StatefulSet{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

func (r *RedisClusterReconciler) PreFilter() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return r.isOwnedByUs(e.ObjectNew)
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return r.isOwnedByUs(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return r.isOwnedByUs(e.Object)
		},
	}
}

func (r *RedisClusterReconciler) SetConditionTrue(rc *redisv1.RedisCluster, condition metav1.Condition, message string) {
	if !meta.IsStatusConditionTrue(rc.Status.Conditions, condition.Type) {
		meta.SetStatusCondition(&rc.Status.Conditions, condition)
		r.Recorder.Event(rc, "Normal", condition.Reason, message)
	}
}

func (r *RedisClusterReconciler) LogInfo(RCNamespacedName types.NamespacedName, msg string, keysAndValues ...interface{}) {
	redisClusterInfo := []interface{}{"redis-cluster", RCNamespacedName}
	r.Log.Info(msg, append(redisClusterInfo, keysAndValues...)...)
}

func (r *RedisClusterReconciler) LogError(RCNamespacedName types.NamespacedName, err error, msg string, keysAndValues ...interface{}) {
	redisClusterInfo := []interface{}{"redis-cluster", RCNamespacedName}
	r.Log.Error(err, msg, append(redisClusterInfo, keysAndValues...)...)
}

func (r *RedisClusterReconciler) GetHelperLogger(RCNamespacedName types.NamespacedName) logr.Logger {
	return r.Log.WithValues("redis-cluster", RCNamespacedName)
}
