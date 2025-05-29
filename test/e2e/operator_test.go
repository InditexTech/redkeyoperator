// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	// Define a default value for the OPERATOR_IMAGE in case the environment variable is not set.
	defaultOperatorImageLocal = "redis-operator:0.1.0"
)

func getOperatorImageFromEnv() string {
	operatorImage := os.Getenv("OPERATOR_IMAGE")
	if operatorImage == "" {
		// Use the default value if the environment variable is not set.
		operatorImage = defaultOperatorImageLocal
	}
	return operatorImage
}

func ensureOperatorExistsOrCreate(nsName types.NamespacedName) error {
	// rc := &appsv1.Deployment{}
	rcOperator, err := createOperator()
	if err != nil {
		return err
	}
	err = k8sClient.Get(context.TODO(), nsName, &rcOperator)
	if err != nil {
		if err := k8sClient.Create(context.TODO(), &rcOperator); err != nil {
			return err
		}
	}
	return nil
}

func ensureServiceAccountExistsOrCreate(nsName types.NamespacedName) error {
	serviceAccount := createServiceAccount()

	sa := &corev1.ServiceAccount{}
	err := k8sClient.Get(context.TODO(), nsName, sa)
	if err != nil {
		if err := k8sClient.Create(context.TODO(), &serviceAccount); err != nil {
			return err
		}
	}

	return nil
}

func ensureLeaderElectionRoleExistsOrCreate(nsName types.NamespacedName) error {
	r := &rbacv1.Role{}
	leaderElectionRole := createLeaderElectionRole()
	err := k8sClient.Get(context.TODO(), nsName, r)

	if err != nil {
		if err := k8sClient.Create(context.TODO(), &leaderElectionRole); err != nil {
			return err
		}
	}
	return nil
}

func ensureRedisOperatorRoleExistsOrCreate(nsName types.NamespacedName) error {
	r := &rbacv1.Role{}
	redisOperatorRole := createRedisOperatorRole()
	err := k8sClient.Get(context.TODO(), nsName, r)

	if err != nil {
		if err := k8sClient.Create(context.TODO(), &redisOperatorRole); err != nil {
			return err
		}
	}
	return nil
}

func ensureLeaderElectionRoleBindingExistsOrCreate(nsName types.NamespacedName) error {
	rb := &rbacv1.RoleBinding{}
	leaderElectionRoleBinding := createLeaderElectionRoleBinding()
	err := k8sClient.Get(context.TODO(), nsName, rb)

	if err != nil {
		if err := k8sClient.Create(context.TODO(), &leaderElectionRoleBinding); err != nil {
			return err
		}
	}
	return nil
}

func ensureRedisOperatorRoleBindingExistsOrCreate(nsName types.NamespacedName) error {
	rb := &rbacv1.RoleBinding{}
	redisOperatorRoleBinding := createRedisOperatorRoleBinding()
	err := k8sClient.Get(context.TODO(), nsName, rb)

	if err != nil {
		if err := k8sClient.Create(context.TODO(), &redisOperatorRoleBinding); err != nil {
			return err
		}
	}
	return nil
}

func ensureConfigMapExistsOrCreate(nsName types.NamespacedName) error {
	cm := &corev1.ConfigMap{}
	configMap := createConfigMap()
	err := k8sClient.Get(context.TODO(), nsName, cm)
	if err != nil {
		if err := k8sClient.Create(context.TODO(), &configMap); err != nil {
			return err
		}
	}
	return nil
}

func createOperator() (appsv1.Deployment, error) {

	toCreate := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator",
			Namespace: RedisNamespace,
			Labels: map[string]string{
				"control-plane": "redis-operator",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: func() *int32 { i := int32(1); return &i }(),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"control-plane": "redis-operator",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"control-plane": "redis-operator",
						"domain":        "DOMAIN",
						"environment":   "ENVIRONMENT",
						"layer":         "middleware-redisoperator",
						"slot":          "default",
						"tenant":        "TENANT",
						"type":          "middleware",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis-operator",
							Image: getOperatorImageFromEnv(),
							Args: []string{
								"--leader-elect",
								"--max-concurrent-reconciles",
								"10",
							},
							Command: []string{"/manager"},
							Env: []corev1.EnvVar{
								{
									Name:  "WATCH_NAMESPACE",
									Value: RedisNamespace,
								},
							},
							ImagePullPolicy: corev1.PullIfNotPresent,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("300m"),
									corev1.ResourceMemory: resource.MustParse("300Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("200Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: func() *bool { b := false; return &b }(),
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: func() *bool { b := true; return &b }(),
					},
					ServiceAccountName:            "redis-operator-sa",
					TerminationGracePeriodSeconds: func() *int64 { i := int64(10); return &i }(),
				},
			},
		},
	}

	return toCreate, nil
}

func createServiceAccount() corev1.ServiceAccount {
	serviceAccount := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator-sa",
			Namespace: RedisNamespace,
		},
	}

	return serviceAccount
}

func createLeaderElectionRole() rbacv1.Role {
	role := rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "leader-election-role",
			Namespace: RedisNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"", "coordination.k8s.io"},
				Resources: []string{"configmaps", "leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch"},
			},
		},
	}

	return role
}

func createRedisOperatorRole() rbacv1.Role {
	role := rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator-role",
			Namespace: RedisNamespace,
			Labels: map[string]string{
				"rbac.authorization.k8s.io/aggregate-to-admin": "true",
			},
		},
		Rules: []rbacv1.PolicyRule{
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
				Resources: []string{"redisclusters"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			},
			{
				APIGroups: []string{"redis.inditex.dev"},
				Resources: []string{"redisclusters/finalizers"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{"redis.inditex.dev"},
				Resources: []string{"redisclusters/status"},
				Verbs:     []string{"get", "patch", "update"},
			},
		},
	}

	return role
}

func createLeaderElectionRoleBinding() rbacv1.RoleBinding {
	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "leader-election-rolebinding",
			Namespace: RedisNamespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "leader-election-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "redis-operator-sa",
				Namespace: RedisNamespace,
			},
		},
	}

	return roleBinding
}

func createRedisOperatorRoleBinding() rbacv1.RoleBinding {
	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator-rolebinding",
			Namespace: RedisNamespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "redis-operator-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "redis-operator-sa",
				Namespace: RedisNamespace,
			},
		},
	}

	return roleBinding
}

func createConfigMap() corev1.ConfigMap {
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-operator-config",
			Namespace: RedisNamespace,
		},
		Data: map[string]string{
			"redis_operator_config.yaml": `
				apiVersion: controller-runtime.sigs.k8s.io/v1alpha1
				kind: ControllerManagerConfig
				health:
				healthProbeBindAddress: :8081
				metrics:
				bindAddress: 127.0.0.1:8080
				webhook:
				port: 9443
				leaderElection:
				leaderElect: true
				resourceName: db95d8a6.inditex.dev
				`,
		},
	}

	return configMap
}
