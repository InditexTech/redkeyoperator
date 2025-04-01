package finalizer

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConfigMapCleanupFinalizer struct {
}

func (ef *ConfigMapCleanupFinalizer) DeleteMethod(ctx context.Context, redis *redisv1.RedisCluster, client client.Client) error {
	err := client.Delete(ctx, &corev1.ConfigMap{
		ObjectMeta: v1.ObjectMeta{Name: redis.GetName(), Namespace: redis.GetNamespace()},
	})
	return err
}

func (ef *ConfigMapCleanupFinalizer) GetId() string {
	return "redis.inditex.com/configmap-cleanup"
}
