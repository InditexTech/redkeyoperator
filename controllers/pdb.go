// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"github.com/inditextech/redisoperator/internal/kubernetes"
	redis "github.com/inditextech/redisoperator/internal/redis"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *RedisClusterReconciler) FindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return r.FindExistingPodDisruptionBudgetFunc(ctx, req)
}

func (r *RedisClusterReconciler) DoFindExistingPodDisruptionBudget(ctx context.Context, req ctrl.Request) (*pv1.PodDisruptionBudget, error) {
	return kubernetes.FindExistingPodDisruptionBudget(ctx, r.Client, req)
}

func (r *RedisClusterReconciler) createPodDisruptionBudget(req ctrl.Request, redisCluster *redisv1.RedisCluster, labels map[string]string) *pv1.PodDisruptionBudget {
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

func (r *RedisClusterReconciler) updatePodDisruptionBudget(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	if !redisCluster.Spec.Pdb.Enabled || redisCluster.Spec.Replicas == 1 {
		return nil
	}

	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redisCluster.Namespace, Name: redisCluster.Name + "-pdb"}, refreshedPdb)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		if redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal != 0 || redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal != "" {
			refreshedPdb.Spec.MinAvailable = nil
			refreshedPdb.Spec.MaxUnavailable = &redisCluster.Spec.Pdb.PdbSizeUnavailable
		} else {
			refreshedPdb.Spec.MaxUnavailable = nil
			refreshedPdb.Spec.MinAvailable = &redisCluster.Spec.Pdb.PdbSizeAvailable
		}
		refreshedPdb.ObjectMeta.Labels = *redisCluster.Spec.Labels
		refreshedPdb.Spec.Selector.MatchLabels = map[string]string{redis.RedisClusterLabel: redisCluster.ObjectMeta.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"}

		var updateErr = r.Client.Update(ctx, refreshedPdb)
		return updateErr
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *RedisClusterReconciler) checkAndManagePodDisruptionBudget(ctx context.Context, req ctrl.Request, redisCluster *redisv1.RedisCluster) {
	if redisCluster.Spec.Pdb.Enabled && redisCluster.Spec.Replicas > 1 {
		_, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err != nil {
			if errors.IsNotFound(err) {
				// Create PodDisruptionBudget
				pdb := r.createPodDisruptionBudget(req, redisCluster, *redisCluster.Spec.Labels)
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
		r.deletePodDisruptionBudget(ctx, redisCluster)
	}
}

func (r *RedisClusterReconciler) checkPDB(ctx context.Context, redisCluster *redisv1.RedisCluster) error {
	// Check if the pdb availables are changed
	if redisCluster.Spec.Pdb.Enabled && redisCluster.Spec.Replicas > 1 {
		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to get PodDisruptionBudget")
		}
		if pdb != nil {
			maxUnavailableFromRedisCluster := &redisCluster.Spec.Pdb.PdbSizeUnavailable
			minAvailableFromRedisCluster := &redisCluster.Spec.Pdb.PdbSizeAvailable
			if redisCluster.Spec.Pdb.PdbSizeAvailable.IntVal == 0 && redisCluster.Spec.Pdb.PdbSizeAvailable.StrVal == "" {
				minAvailableFromRedisCluster = nil
			}
			if redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal == 0 && redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal == "" {
				maxUnavailableFromRedisCluster = nil
			}
			if maxUnavailableFromRedisCluster != nil {
				if pdb.Spec.MaxUnavailable != nil {
					if pdb.Spec.MaxUnavailable.IntVal != redisCluster.Spec.Pdb.PdbSizeUnavailable.IntVal || pdb.Spec.MaxUnavailable.StrVal != redisCluster.Spec.Pdb.PdbSizeUnavailable.StrVal {
						r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedisCluster)
						redisCluster.Status.Status = redisv1.StatusConfiguring
						r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
						return nil
					}
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PBD changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedisCluster)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}

			} else if minAvailableFromRedisCluster != nil {
				if pdb.Spec.MinAvailable != nil {
					if pdb.Spec.MinAvailable.IntVal != redisCluster.Spec.Pdb.PdbSizeAvailable.IntVal || pdb.Spec.MinAvailable.StrVal != redisCluster.Spec.Pdb.PdbSizeAvailable.StrVal {
						r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedisCluster)
						redisCluster.Status.Status = redisv1.StatusConfiguring
						r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
						return nil
					}
				} else {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedisCluster)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}
			}
			// Selector match labels check
			desiredLabels := map[string]string{redis.RedisClusterLabel: redisCluster.ObjectMeta.Name, r.GetStatefulSetSelectorLabel(redisCluster): "redis"}
			if len(pdb.Spec.Selector.MatchLabels) != len(desiredLabels) {
				r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
				redisCluster.Status.Status = redisv1.StatusConfiguring
				r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
				return nil
			}
			for k, v := range desiredLabels {
				if pdb.Spec.Selector.MatchLabels[k] != v {
					r.LogInfo(redisCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
					redisCluster.Status.Status = redisv1.StatusConfiguring
					r.Recorder.Event(redisCluster, "Normal", "UpdatePdb", "Cluster configuring pdb by update")
					return nil
				}
			}
		}
	} else {
		// Delete PodDisruptionBudget if exists
		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
		if err == nil && pdb != nil {
			pdb.Name = redisCluster.Name + "-pdb"
			pdb.Namespace = redisCluster.Namespace
			err := r.deleteExistingPodDisruptionBudget(ctx, pdb, redisCluster)
			if err != nil {
				r.LogError(redisCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
			} else {
				r.LogInfo(redisCluster.NamespacedName(), "Existing PodDisruptionBudget Deleted")
			}
		}
	}
	return nil
}

func (r *RedisClusterReconciler) deletePodDisruptionBudget(ctx context.Context, redisCluster *redisv1.RedisCluster) {
	// Delete PodDisruptionBudget
	pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redisCluster.Name + "-pdb", Namespace: redisCluster.Namespace}})
	if err != nil {
		r.LogInfo(redisCluster.NamespacedName(), "PodDisruptionBudget not deployed", "PodDisruptionBudget Name", redisCluster.Name+"-pdb")
	} else {
		err = r.deleteExistingPodDisruptionBudget(ctx, pdb, redisCluster)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
		} else {
			r.LogInfo(redisCluster.NamespacedName(), "PodDisruptionBudget Deleted")
		}
	}
}

func (r *RedisClusterReconciler) deleteExistingPodDisruptionBudget(ctx context.Context, pdb *pv1.PodDisruptionBudget, redisCluster *redisv1.RedisCluster) error {
	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh rediscluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: pdb.Namespace, Name: pdb.Name}, refreshedPdb)
		if err != nil {
			r.LogError(redisCluster.NamespacedName(), err, "Error getting a refreshed RedisCluster before updating it. It may have been deleted?")
			return err
		}
		var updateErr = r.Client.Delete(ctx, refreshedPdb)
		return updateErr
	})
	if err != nil {
		return err
	}
	return nil
}
