package config

import (
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newCachingClient is an alternative implementation of controller-runtime's
// default client for manager.Manager.
// The only difference is that this implementation sets `CacheUnstructured` to `true` to
// cache unstructured objects.
func newCachingClient(config *rest.Config, options client.Options) (client.Client, error) {
	options.Cache.Unstructured = true
	return client.New(config, options)
}

func NewClient() client.NewClientFunc {
	return func(config *rest.Config, options client.Options) (client.Client, error) {
		c, err := newCachingClient(config, options)
		return c, err
	}
}

