package config

import (
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// newCachingClient is an alternative implementation of controller-runtime's
// default client for manager.Manager.
// The only difference is that this implementation sets `CacheUnstructured` to `true` to
// cache unstructured objects.
func newCachingClient(cache cache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
	c, err := client.New(config, options)
	if err != nil {
		return nil, err
	}

	return client.NewDelegatingClient(client.NewDelegatingClientInput{
		CacheReader:       cache,
		Client:            c,
		UncachedObjects:   uncachedObjects,
		CacheUnstructured: true,
	})
}

func NewClient() cluster.NewClientFunc {
	return func(cache cache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
		c, err := newCachingClient(cache, config, options, uncachedObjects...)
		return c, err
	}
}
