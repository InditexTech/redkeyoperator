// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1client

import (
	"context"
	v1alpha1 "github.com/inditextech/redisoperator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// RdclInterface defines the methods to be implemented by Redis Clients
type RdclInterface interface {
	List(ctx context.Context, opts metav1.ListOptions) (*v1alpha1.RedisClusterList, error)
	Get(ctx context.Context, name string, options metav1.GetOptions) (*v1alpha1.RedisCluster, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

type rdclClient struct {
	restClient rest.Interface
	ns         string
}

func (c *rdclClient) List(ctx context.Context, opts metav1.ListOptions) (*v1alpha1.RedisClusterList, error) {
	result := v1alpha1.RedisClusterList{}
	err := c.restClient.
		Get().
		AbsPath("/apis/redis.inditex.com/v1alpha1").
		Namespace(c.ns).
		Resource("redisclusters").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do(ctx).
		Into(&result)

	return &result, err
}

func (c *rdclClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*v1alpha1.RedisCluster, error) {
	result := v1alpha1.RedisCluster{}
	err := c.restClient.
		Get().
		AbsPath("/apis/redis.inditex.com/v1alpha1").
		Namespace(c.ns).
		Resource("redisclusters").
		Name(name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Do(ctx).
		Into(&result)
	return &result, err
}

func (c *rdclClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return c.restClient.Get().
		AbsPath("/apis/redis.inditex.com/v1alpha1").
		Namespace(c.ns).
		Resource("redisclusters").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch(ctx)
}

// V1Alpha1Interface defines the interface to communicate with all GroupVersion. It now just bears a client fdr Redis Clusters
type V1Alpha1Interface interface {
	RedisClusters(namespace string) RdclInterface
}

// V1Alpha1Client is the struct that bears the rest Interface. It implements RedisClusters method, which satisfies the V1Alpha1Interface
type V1Alpha1Client struct {
	restClient rest.Interface
}

// NewForConfig creates V1Alpha1Client by using the given rest.Config. Returns error if something is amiss in the config.
func NewForConfig(c *rest.Config) (*V1Alpha1Client, error) {
	config := *c
	config.ContentConfig.GroupVersion = &schema.GroupVersion{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version}
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	config.UserAgent = rest.DefaultKubernetesUserAgent()

	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, err
	}

	return &V1Alpha1Client{restClient: client}, nil
}

// RedisClusters returns the interface which will allow the caller to access the implemented methods of the interface
func (c *V1Alpha1Client) RedisClusters(namespace string) RdclInterface {
	return &rdclClient{
		restClient: c.restClient,
		ns:         namespace,
	}
}
