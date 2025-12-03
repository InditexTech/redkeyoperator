// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

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

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	finalizer "github.com/inditextech/redkeyoperator/internal/finalizers"
)

// RedkeyClusterReconciler reconciles a RedkeyCluster object
type RedkeyClusterReconciler struct {
	client.Client
	Log                                 logr.Logger
	Scheme                              *runtime.Scheme
	Recorder                            record.EventRecorder
	Finalizers                          []finalizer.Finalizer
	MaxConcurrentReconciles             int
	ConcurrentMigrate                   int
	GetReadyNodesFunc                   func(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) (map[string]*redkeyv1.RedisNode, error)
	FindExistingStatefulSetFunc         func(ctx context.Context, req ctrl.Request) (*v1.StatefulSet, error)
	FindExistingConfigMapFunc           func(ctx context.Context, req ctrl.Request) (*corev1.ConfigMap, error)
	FindExistingDeploymentFunc          func(ctx context.Context, req ctrl.Request) (*v1.Deployment, error)
	FindExistingPodDisruptionBudgetFunc func(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error)
}

// +kubebuilder:rbac:groups=redis.inditex.dev,resources=redkeyclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redis.inditex.dev,resources=redkeyclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redis.inditex.dev,resources=redkeyclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmap;services;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;create;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=create;delete;patch;update
func (r *RedkeyClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("RedkeyCluster reconciler called", "redis-cluster", req.NamespacedName, "name", req.Name, "ns", req.Namespace)
	redkeyCluster := &redkeyv1.RedkeyCluster{}
	err := r.Client.Get(ctx, req.NamespacedName, redkeyCluster)
	if err == nil {
		r.Log.Info("Found RedkeyCluster", "redis-cluster", req.NamespacedName, "name", redkeyCluster.GetName(), "GVK", redkeyCluster.GroupVersionKind().String(), "status", redkeyCluster.Status.Status)
		return r.ReconcileClusterObject(ctx, req, redkeyCluster)
	} else {
		// cluster deleted
		r.Log.Info("Can't find RedkeyCluster, probably deleted", "redis-cluster", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedkeyClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redkeyv1.RedkeyCluster{}).
		Watches(&corev1.ConfigMap{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(r.PreFilter())).
		Owns(&v1.StatefulSet{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

func (r *RedkeyClusterReconciler) PreFilter() predicate.Predicate {
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

func (r *RedkeyClusterReconciler) logInfo(RCNamespacedName types.NamespacedName, msg string, keysAndValues ...any) {
	redkeyClusterInfo := []any{"redkey-cluster", RCNamespacedName}
	r.Log.Info(msg, append(redkeyClusterInfo, keysAndValues...)...)
}

func (r *RedkeyClusterReconciler) logError(RCNamespacedName types.NamespacedName, err error, msg string, keysAndValues ...any) {
	redkeyClusterInfo := []any{"redkey-cluster", RCNamespacedName}
	r.Log.Error(err, msg, append(redkeyClusterInfo, keysAndValues...)...)
}

func (r *RedkeyClusterReconciler) getHelperLogger(RCNamespacedName types.NamespacedName) logr.Logger {
	return r.Log.WithValues("redkey-cluster", RCNamespacedName)
}
