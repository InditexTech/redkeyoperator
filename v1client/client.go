// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package redisv1client

import (
	"context"

	redkeyv1 "github.com/inditextech/redkeyoperator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// RkclInterface defines the methods to be implemented by Redis Clients
type RkclInterface interface {
	List(ctx context.Context, opts metav1.ListOptions) (*redkeyv1.RedkeyClusterList, error)
	Get(ctx context.Context, name string, options metav1.GetOptions) (*redkeyv1.RedkeyCluster, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

type rkclClient struct {
	restClient rest.Interface
	ns         string
}

func (c *rkclClient) List(ctx context.Context, opts metav1.ListOptions) (*redkeyv1.RedkeyClusterList, error) {
	result := redkeyv1.RedkeyClusterList{}
	err := c.restClient.
		Get().
		AbsPath("/apis/redkey.inditex.dev/redisv1").
		Namespace(c.ns).
		Resource("redkeyclusters").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do(ctx).
		Into(&result)

	return &result, err
}

func (c *rkclClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*redkeyv1.RedkeyCluster, error) {
	result := redkeyv1.RedkeyCluster{}
	err := c.restClient.
		Get().
		AbsPath("/apis/redkey.inditex.dev/v1").
		Namespace(c.ns).
		Resource("redkeyclusters").
		Name(name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Do(ctx).
		Into(&result)
	return &result, err
}

func (c *rkclClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return c.restClient.Get().
		AbsPath("/apis/redkey.inditex.dev/v1").
		Namespace(c.ns).
		Resource("redkeyclusters").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch(ctx)
}

// V1Interface defines the interface to communicate with all GroupVersion. It now just bears a client for Redkey Clusters
type V1Interface interface {
	RedkeyClusters(namespace string) RkclInterface
}

// V1Client is the struct that bears the rest Interface. It implements RedkeyClusters method, which satisfies the redisv1Interface
type V1Client struct {
	restClient rest.Interface
}

// NewForConfig creates V1Client by using the given rest.Config. Returns error if something is amiss in the config.
func NewForConfig(c *rest.Config) (*V1Client, error) {
	config := *c
	config.ContentConfig.GroupVersion = &schema.GroupVersion{Group: redkeyv1.GroupVersion.Group, Version: redkeyv1.GroupVersion.Version}
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	config.UserAgent = rest.DefaultKubernetesUserAgent()

	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, err
	}

	return &V1Client{restClient: client}, nil
}

// RedkeyClusters returns the interface which will allow the caller to access the implemented methods of the interface
func (c *V1Client) RedkeyClusters(namespace string) RkclInterface {
	return &rkclClient{
		restClient: c.restClient,
		ns:         namespace,
	}
}
