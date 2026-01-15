// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultOperatorImage = "redkey-operator:0.1.0"

// getOperatorImage reads OPERATOR_IMAGE or falls back
func getOperatorImage() string {
	if img := os.Getenv("OPERATOR_IMAGE"); img != "" {
		return img
	}
	return defaultOperatorImage
}

// ensureResource creates obj if it doesn’t already exist
func ensureResource(ctx context.Context, obj ctrlclient.Object) error {
	if err := k8sClient.Create(ctx, obj); apierrors.IsAlreadyExists(err) {
		return nil
	} else {
		return err
	}
}

// EnsureOperatorSetup finishes in one shot the ServiceAccount, RBAC, ConfigMap and Deployment
func EnsureOperatorSetup(ctx context.Context, namespace string) error {
	objs := []ctrlclient.Object{
		newServiceAccount(namespace),
		newRole(namespace, "leader-election-role", leaderElectionPolicyRules()),
		newRole(namespace, "redkey-operator-role", operatorPolicyRules()),
		newRoleBinding(namespace, "leader-election-rolebinding", "leader-election-role"),
		newRoleBinding(namespace, "redkey-operator-rolebinding", "redkey-operator-role"),
		newConfigMap(namespace),
		newOperatorDeployment(namespace),
	}
	for _, obj := range objs {
		if err := ensureResource(ctx, obj); err != nil {
			return fmt.Errorf("ensure %T: %w", obj, err)
		}
	}
	return nil
}

func newServiceAccount(ns string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redkey-operator-sa",
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
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "redkey-operator-sa", Namespace: ns}},
	}
}

func newConfigMap(ns string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "redkey-operator-config", Namespace: ns},
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
			Name:      "redkey-operator",
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
					ServiceAccountName:            "redkey-operator-sa",
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
		Name:            "redkey-operator",
		Image:           getOperatorImage(),
		Command:         []string{"/manager"},
		Args:            []string{"--leader-elect", "--max-concurrent-reconciles", "10"},
		Env:             []corev1.EnvVar{{Name: "WATCH_NAMESPACE", Value: ns}},
		ImagePullPolicy: corev1.PullAlways,
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
