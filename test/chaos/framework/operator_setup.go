// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

// NOTE: This file is adapted from test/e2e/operator_test.go for chaos tests.
// It contains the operator setup functions needed to deploy the operator in the test namespace.
package framework

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// EnsureOperatorSetup creates the ServiceAccount, RBAC, ConfigMap and Deployment for the operator.
func EnsureOperatorSetup(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	// Create ServiceAccount
	if _, err := clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, newServiceAccount(namespace), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure ServiceAccount: %w", err)
	}

	// Create Roles
	if _, err := clientset.RbacV1().Roles(namespace).Create(ctx, newRole(namespace, "leader-election-role", leaderElectionPolicyRules()), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure leader-election-role: %w", err)
	}
	if _, err := clientset.RbacV1().Roles(namespace).Create(ctx, newRole(namespace, "redis-operator-role", operatorPolicyRules()), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure redis-operator-role: %w", err)
	}

	// Create RoleBindings
	if _, err := clientset.RbacV1().RoleBindings(namespace).Create(ctx, newRoleBinding(namespace, "leader-election-rolebinding", "leader-election-role"), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure leader-election-rolebinding: %w", err)
	}
	if _, err := clientset.RbacV1().RoleBindings(namespace).Create(ctx, newRoleBinding(namespace, "redis-operator-rolebinding", "redis-operator-role"), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure redis-operator-rolebinding: %w", err)
	}

	// Create ConfigMap
	if _, err := clientset.CoreV1().ConfigMaps(namespace).Create(ctx, newConfigMap(namespace), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure ConfigMap: %w", err)
	}

	// Create Deployment
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, newOperatorDeployment(namespace), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure Deployment: %w", err)
	}

	return nil
}

func newServiceAccount(ns string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator-sa",
			Namespace: ns,
		},
	}
}

func newRole(ns, name string, rules []rbacv1.PolicyRule) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Rules:      rules,
	}
}

func newRoleBinding(ns, name, roleName string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "redis-operator-sa", Namespace: ns}},
	}
}

func newConfigMap(ns string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-operator-config", Namespace: ns},
		Data: map[string]string{
			"redis_operator_config.yaml": `apiVersion: controller-runtime.sigs.k8s.io/v1alpha1
kind: ControllerManagerConfig
health:
  healthProbeBindAddress: ":8081"
metrics:
  bindAddress: "127.0.0.1:8080"
leaderElection:
  leaderElect: true
  resourceName: db95d8a6.inditex.com
`,
		},
	}
}

func newOperatorDeployment(ns string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator",
			Namespace: ns,
			Labels:    map[string]string{"control-plane": "redkey-operator"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"control-plane": "redkey-operator"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"control-plane": "redkey-operator",
						"domain":        "DOMAIN",
						"environment":   "ENVIRONMENT",
						"layer":         "middleware-redkeyoperator",
						"slot":          "default",
						"tenant":        "TENANT",
						"type":          "middleware",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:            "redis-operator-sa",
					SecurityContext:               &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true)},
					TerminationGracePeriodSeconds: ptr.To(int64(10)),
					Containers:                    []corev1.Container{newOperatorContainer(ns)},
				},
			},
		},
	}
}

func newOperatorContainer(ns string) corev1.Container {
	return corev1.Container{
		Name:            "redis-operator",
		Image:           GetOperatorImage(),
		Command:         []string{"/manager"},
		Args:            []string{"--leader-elect", "--max-concurrent-reconciles", "10"},
		Env:             []corev1.EnvVar{{Name: "WATCH_NAMESPACE", Value: ns}},
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false)},
		Resources: corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("500Mi")},
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m"), corev1.ResourceMemory: resource.MustParse("250Mi")},
		},
	}
}

func leaderElectionPolicyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create", "patch"},
		},
	}
}

func operatorPolicyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps", "pods", "services"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps", "services"},
			Verbs:     []string{"create", "update", "delete", "get", "list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"},
			Verbs:     []string{"get", "list", "watch", "delete", "deletecollection"},
		},
		{
			APIGroups: []string{"apps"},
			Resources: []string{"deployments", "statefulsets"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"policy"},
			Resources: []string{"poddisruptionbudgets"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"redis.inditex.dev"},
			Resources: []string{"redkeyclusters"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"redis.inditex.dev"},
			Resources: []string{"redkeyclusters/finalizers"},
			Verbs:     []string{"update"},
		},
		{
			APIGroups: []string{"redis.inditex.dev"},
			Resources: []string{"redkeyclusters/status"},
			Verbs:     []string{"get", "patch", "update"},
		},
	}
}
