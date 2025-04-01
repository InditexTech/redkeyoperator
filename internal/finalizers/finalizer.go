package finalizer

import (
	"context"

	redisv1 "github.com/inditextech/redisoperator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Finalizer interface {
	DeleteMethod(context.Context, *redisv1.RedisCluster, client.Client) error
	GetId() string
}
