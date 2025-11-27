// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	redis "github.com/inditextech/redkeyoperator/internal/redis"
	pv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *RedkeyClusterReconciler) createPodDisruptionBudget(req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster, labels map[string]string) *pv1.PodDisruptionBudget {
	pdb := &pv1.PodDisruptionBudget{}
	if redkeyCluster.Spec.Pdb.PdbSizeUnavailable.StrVal != "" || redkeyCluster.Spec.Pdb.PdbSizeUnavailable.IntVal != 0 {
		pdb = &pv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name + "-pdb",
				Namespace: req.Namespace,
				Labels:    labels,
			},
			Spec: pv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &redkeyCluster.Spec.Pdb.PdbSizeUnavailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{redis.RedkeyClusterLabel: req.Name, r.getStatefulSetSelectorLabel(redkeyCluster): "redis"},
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
				MinAvailable: &redkeyCluster.Spec.Pdb.PdbSizeAvailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{redis.RedkeyClusterLabel: req.Name, r.getStatefulSetSelectorLabel(redkeyCluster): "redis"},
				},
			},
		}
	}

	return pdb
}

func (r *RedkeyClusterReconciler) updatePodDisruptionBudget(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh redkeycluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: redkeyCluster.Namespace, Name: redkeyCluster.Name + "-pdb"}, refreshedPdb)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
			return err
		}
		if redkeyCluster.Spec.Pdb.PdbSizeUnavailable.IntVal != 0 || redkeyCluster.Spec.Pdb.PdbSizeUnavailable.StrVal != "" {
			refreshedPdb.Spec.MinAvailable = nil
			refreshedPdb.Spec.MaxUnavailable = &redkeyCluster.Spec.Pdb.PdbSizeUnavailable
		} else {
			refreshedPdb.Spec.MaxUnavailable = nil
			refreshedPdb.Spec.MinAvailable = &redkeyCluster.Spec.Pdb.PdbSizeAvailable
		}
		refreshedPdb.ObjectMeta.Labels = *redkeyCluster.Spec.Labels
		refreshedPdb.Spec.Selector.MatchLabels = map[string]string{redis.RedkeyClusterLabel: redkeyCluster.ObjectMeta.Name, r.getStatefulSetSelectorLabel(redkeyCluster): "redis"}

		var updateErr = r.Client.Update(ctx, refreshedPdb)
		return updateErr
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *RedkeyClusterReconciler) checkAndManagePodDisruptionBudget(ctx context.Context, req ctrl.Request, redkeyCluster *redkeyv1.RedkeyCluster) {
	if redkeyCluster.Spec.Pdb.Enabled && redkeyCluster.Spec.Primaries > 1 {
		_, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-pdb", Namespace: redkeyCluster.Namespace}})
		if err != nil {
			if errors.IsNotFound(err) {
				// Create PodDisruptionBudget
				pdb := r.createPodDisruptionBudget(req, redkeyCluster, *redkeyCluster.Spec.Labels)
				ctrl.SetControllerReference(redkeyCluster, pdb, r.Scheme)
				pdbCreateErr := r.Client.Create(ctx, pdb)
				if pdbCreateErr != nil {
					r.logError(redkeyCluster.NamespacedName(), pdbCreateErr, "Error creating PodDisruptionBudget")
				}
			} else {
				r.logError(redkeyCluster.NamespacedName(), err, "Error getting existing PodDisruptionBudget")
			}
		}
	}
	if redkeyCluster.Spec.Primaries == 1 || !redkeyCluster.Spec.Pdb.Enabled {
		r.deletePodDisruptionBudget(ctx, redkeyCluster)
	}
}

func (r *RedkeyClusterReconciler) checkAndUpdatePodDisruptionBudget(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) error {
	if redkeyCluster.Spec.Pdb.Enabled && redkeyCluster.Spec.Primaries > 1 {
		// Check if the pdb availables are changed

		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-pdb", Namespace: redkeyCluster.Namespace}})
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Failed to get PodDisruptionBudget")
		}
		if pdb != nil {
			proceedToUpdate := false
			maxUnavailableFromRedkeyCluster := &redkeyCluster.Spec.Pdb.PdbSizeUnavailable
			minAvailableFromRedkeyCluster := &redkeyCluster.Spec.Pdb.PdbSizeAvailable
			if redkeyCluster.Spec.Pdb.PdbSizeAvailable.IntVal == 0 && redkeyCluster.Spec.Pdb.PdbSizeAvailable.StrVal == "" {
				minAvailableFromRedkeyCluster = nil
			}
			if redkeyCluster.Spec.Pdb.PdbSizeUnavailable.IntVal == 0 && redkeyCluster.Spec.Pdb.PdbSizeUnavailable.StrVal == "" {
				maxUnavailableFromRedkeyCluster = nil
			}
			if maxUnavailableFromRedkeyCluster != nil {
				if pdb.Spec.MaxUnavailable != nil {
					if pdb.Spec.MaxUnavailable.IntVal != redkeyCluster.Spec.Pdb.PdbSizeUnavailable.IntVal || pdb.Spec.MaxUnavailable.StrVal != redkeyCluster.Spec.Pdb.PdbSizeUnavailable.StrVal {
						r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedkeyCluster)
						proceedToUpdate = true
					}
				} else {
					r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PBD changed update pdb deployed", "OldMaxUnavailable", pdb.Spec.MaxUnavailable, "NewMaxUnavailable", maxUnavailableFromRedkeyCluster)
					proceedToUpdate = true
				}

			} else if minAvailableFromRedkeyCluster != nil {
				if pdb.Spec.MinAvailable != nil {
					if pdb.Spec.MinAvailable.IntVal != redkeyCluster.Spec.Pdb.PdbSizeAvailable.IntVal || pdb.Spec.MinAvailable.StrVal != redkeyCluster.Spec.Pdb.PdbSizeAvailable.StrVal {
						r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedkeyCluster)
						proceedToUpdate = true
					}
				} else {
					r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB changed update pdb deployed", "OldMinAvailable", pdb.Spec.MinAvailable, "NewMinAvailable", minAvailableFromRedkeyCluster)
					proceedToUpdate = true
				}
			}
			// Selector match labels check
			desiredLabels := map[string]string{redis.RedkeyClusterLabel: redkeyCluster.ObjectMeta.Name, r.getStatefulSetSelectorLabel(redkeyCluster): "redis"}
			if len(pdb.Spec.Selector.MatchLabels) != len(desiredLabels) {
				r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
				proceedToUpdate = true
			}
			for k, v := range desiredLabels {
				if pdb.Spec.Selector.MatchLabels[k] != v {
					r.logInfo(redkeyCluster.NamespacedName(), "Cluster Configured Issued", "reason", "PDB selector match labels", "existing labels", pdb.Spec.Selector.MatchLabels, "desired labels", desiredLabels)
					proceedToUpdate = true
				}
			}

			if proceedToUpdate {
				r.Recorder.Event(redkeyCluster, "Normal", "UpdatePdb", "PDB configuration updated")
				return r.updatePodDisruptionBudget(ctx, redkeyCluster)

			}
		}
	} else {
		// Delete PodDisruptionBudget if exists
		pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-pdb", Namespace: redkeyCluster.Namespace}})
		if err == nil && pdb != nil {
			pdb.Name = redkeyCluster.Name + "-pdb"
			pdb.Namespace = redkeyCluster.Namespace
			err := r.deleteExistingPodDisruptionBudget(ctx, pdb, redkeyCluster)
			if err != nil {
				r.logError(redkeyCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
			} else {
				r.logInfo(redkeyCluster.NamespacedName(), "Existing PodDisruptionBudget Deleted")
			}
		}
	}
	return nil
}

func (r *RedkeyClusterReconciler) deletePodDisruptionBudget(ctx context.Context, redkeyCluster *redkeyv1.RedkeyCluster) {
	// Delete PodDisruptionBudget
	pdb, err := r.FindExistingPodDisruptionBudgetFunc(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: redkeyCluster.Name + "-pdb", Namespace: redkeyCluster.Namespace}})
	if err != nil {
		r.logInfo(redkeyCluster.NamespacedName(), "PodDisruptionBudget not deployed", "PodDisruptionBudget Name", redkeyCluster.Name+"-pdb")
	} else {
		err = r.deleteExistingPodDisruptionBudget(ctx, pdb, redkeyCluster)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Failed to delete PodDisruptionBudget")
		} else {
			r.logInfo(redkeyCluster.NamespacedName(), "PodDisruptionBudget Deleted")
		}
	}
}

func (r *RedkeyClusterReconciler) deleteExistingPodDisruptionBudget(ctx context.Context, pdb *pv1.PodDisruptionBudget, redkeyCluster *redkeyv1.RedkeyCluster) error {
	refreshedPdb := &pv1.PodDisruptionBudget{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// get a fresh redkeycluster to minimize conflicts
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: pdb.Namespace, Name: pdb.Name}, refreshedPdb)
		if err != nil {
			r.logError(redkeyCluster.NamespacedName(), err, "Error getting a refreshed RedkeyCluster before updating it. It may have been deleted?")
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
