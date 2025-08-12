# Developer Guide

Quickly provision Redis cluster environments in Kubernetes or Openshift.

The operator relies on Redis cluster functionality to serve client requests.

## Local development and testing

A *local K8s cluster* can be used to deploy the RedKey Operator from a pre-built image or directly compiling from the source code.

This will be helpfull to develop and test new features, fix bugs, and test releases locally.

As we will see below, you can use the `make` command to deploy the different components, as well as to deploy a sample Redis Cluster, or use the scripts that automate the basic workflows.

## Development profiles

Three **Deployment Profiles** have been defined. Basically, these profiles determine which image will be used to deploy the RedKey Operator pod. These are the 3 Deployment Profiles and the default images used to create the RedKey Operator pod:

| Profile | Image used to create the RedKey Operator pod | Purpose                                         |
|---------|---------------------------------------------|-------------------------------------------------|
| debug   | delve:1.24.5                                | Debug code from your IDE using Delve            |
| dev     | redkey-operator:1.3.0                        | Test a locally built (from source code) release |

## Create your Kubernetes cluster

You can deploy your own Kubernetes cluster locally to develop RedKey Operator with the tool of your choice (Docker Desktop, Rancher Desktop, K3D, Kind,...).

We recommend you to use Kind. To start a local registry and a K8S cluster with Kind you can use the following script (from Kind official doc page):

```bash
#!/bin/sh
set -o errexit

# 1. Create registry container unless it already exists
reg_name='kind-registry'
reg_port='5001'
if [ "$(docker inspect -f '{{.State.Running}}' "${reg_name}" 2>/dev/null || true)" != 'true' ]; then
  docker run \
    -d --restart=always -p "127.0.0.1:${reg_port}:5000" --name "${reg_name}" \
    registry:2
fi

# 2. Create kind cluster with containerd registry config dir enabled
# TODO: kind will eventually enable this by default and this patch will
# be unnecessary.
#
# See:
# https://github.com/kubernetes-sigs/kind/issues/2875
# https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
# See: https://github.com/containerd/containerd/blob/main/docs/hosts.md
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
EOF

# 3. Add the registry config to the nodes
#
# This is necessary because localhost resolves to loopback addresses that are
# network-namespace local.
# In other words: localhost in the container is not localhost on the host.
#
# We want a consistent name that works from both ends, so we tell containerd to
# alias localhost:${reg_port} to the registry container when pulling images
REGISTRY_DIR="/etc/containerd/certs.d/localhost:${reg_port}"
for node in $(kind get nodes); do
  docker exec "${node}" mkdir -p "${REGISTRY_DIR}"
  cat <<EOF | docker exec -i "${node}" cp /dev/stdin "${REGISTRY_DIR}/hosts.toml"
[host."http://${reg_name}:5000"]
EOF
done

# 4. Connect the registry to the cluster network if not already connected
# This allows kind to bootstrap the network but ensures they're on the same network
if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${reg_name}")" = 'null' ]; then
  docker network connect "kind" "${reg_name}"
fi

# 5. Document the local registry
# https://github.com/kubernetes/enhancements/tree/master/keps/sig-cluster-lifecycle/generic/1755-communicating-a-local-registry
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${reg_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
#!/bin/sh
set -o errexit
```

## RedKey Operator

### Deploy RedKey Operator from a custom image

Once your K8s cluster and registry are ready to work with, you need to make available the image you want to use to deploy RedKey Operator.

Your cluster must be configured to be able to access your local registry.

We provide and easy way to build and push the images for `dev` and `debug` profiles:

- `make docker-build`: builds an image containing the RedKey Operator manager built from the source code. This image is published in Docker local registry.
- `make docker-push`: pushes the image built with the command above to the corresponding registry.
- `make debug-docker-build`: builds an image that will allow us to create an *empty* pod as the RedKey operator to which we will copy the manager binary and run it, as we'll explain later.
- `make debug-docker-push`: pushes the image built with the command above to he corresponding registry.

**To test a released RedKey Operator version you'll have to manually pull the image, tag and push to your local registry.**

The image names used by default by each profile (shown in the table above) can be overwritten using the environment variables:

- `IMG`: overwrittes the image name when using `dev` profile.
- `IMG_DEBUG`: overwrittes the image name when using `debug` profile.

E.G. these are the commands to build and push the image for `debug` profile:

```shell
make debug-docker-build IMG_DEBUG=localhost:5001/redkey-operator:delve
make debug-docker-push IMG_DEBUG=localhost:5001/redkey-operator:delve
```

E.G. the commands when using `dev` profile:

```shell
make debug-docker-build IMG_DEV=localhost:5001/redkey-operator:0.1.0
make debug-docker-push IMG_DEV=localhost:5001/redkey-operator:0.1.0
```

Once the RedKey Operator is available in your local registry, you can follow these steps to deploy it into you K8s cluster:

1. Install the CRD.

```shell
make install
```

2. Create the namespace in which you want you RedKey Operator to be deployed. By default, `make` uses `redkey-operator`. Is you want to use a different namespace you should use the `NAMESPACE` environment variable to overwritte this value with the one of your choice.

```shell
kubectl create ns redkey-operator
```

>**The default namespace used by make command is `redkey-operator`. This value can be overwritten using the `NAMESPACE` environment variable.**

3. Generate the manifests, according to the profile you choose to use, to deploy the RedKey Operator. Use the `PROFILE` environment variable to define the profile to use. The manifests are generated in `deployment` directory.

```shell
make process-manifests PROFILE=debug IMG_DEBUG="localhost:5001/redkey-operator:delve"
```

>**If no `PROFILE` environment variable defined, the default value is `dev`.**

4. Deploy the RedKey Operator. Uses the `deployment/deployment.yml` generated in the above step.

```shell
make deploy
```

>**`make` will install the needed tools like `kustomize`, `control-gen` and `envtest` if not available.**

### Debuging RedKey Operator

If you followed the steps described above to deploy the RedKey Operator using the `debug` profile you'll have the CRD deployed and a redkey-operator pod running.

This pod is created using a `golang` image with `Delve` installed on it. This will allow us to easily debug the manager code following these steps:

1. Build the manager binary file from the source code.
2. Copy the manager binary file to the redkey-operator pod.
3. Run the manager binary inside the redkey-operator pod, using Delve to allow remote debugging connections.
4. Port forward `40000` port to be able to connect to the exposed port in the redkey-operator pod.
5. Connect from the IDE of your choice to remote debugging

To follow the 3 first steps simply execute:

```shell
make debug
```

The manager will the be running in your redkey-operator pod and you'll see pod's standard output printing in your terminal. The operator logs will then be streamed to the terminal.

To enable the port forwarding:

```shell
make port-forward
```

You can now attach your local debug from the IDE of your choice to the debug session in the redkey-operator pod. As an example, you'll find the configuration needed to launch the debug from VSCode here:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Connect to server",
            "type": "go",
            "request": "attach",
            "mode": "remote",
            "remotePath": "${workspaceFolder}",
            "port": 40000,
            "host": "127.0.0.1",
            "trace": "verbose",
            "env": {
                "WATCH_NAMESPACE": "default",
                "KUBERNETES_CONFIG":  "${HOME}/.kube/config",
            }
        }
    ]
}
```

Customize the configuration to use your kubeconfig and your namespace if needed.

Before redeploying the operator code using `make debug` you can delete the operator pod using `make delete-operator`. The operator pod will be deleted and automatically recreated to suit the replica set requirements using the debugging image.

>**!! Go 1.16 or above is needed to use Delve debugging !!**

## RedKey Operator Webhook

### Deploy RedKey Operator Webhook from a custom image

Once your K8s cluster and registry are ready to work with, you need to make available the image you want to use to deploy RedKey Operator Webhook.

Your cluster must be configured to be able to access your local registry.

We provide and easy way to build and push the images for `dev` and `debug` profiles:

- `make docker-build-webhook`: builds an image containing the RedKey Operator webhook built from the source code. This image is published in Docker local registry.
- `make docker-push-webhook`: pushes the image built with the command above to the corresponding registry.
- `make debug-docker-build`: builds an image that will allow us to create an *empty* pod as the RedKey operator to which we will copy the manager binary and run it, as we'll explain later.
- `make debug-docker-push`: pushes the image built with the command above to he corresponding registry.

**To test a released RedKey Operator Webhook version you'll have to manually pull the image, tag and push to your local registry.**

The image names used by default by each profile (shown in the table above) can be overwritten using the environment variables:

- `IMG_WEBHOOK`: overwrittes the image name when using `dev` profile.
- `IMG_DEBUG`: overwrittes the image name when using `debug` profile.

E.G. these are the commands to build and push the image for `debug` profile:

```shell
make debug-docker-build IMG_DEBUG=localhost:5001/redkey-operator-webhook:delve
make debug-docker-push IMG_DEBUG=localhost:5001/redkey-operator-webhook:delve
```

E.G. the commands when using `dev` profile:

```shell
make debug-docker-build-webhook IMG_DEV_WEBHOOK=localhost:5001/redkey-operator-webhook:0.1.0
make debug-docker-push-webhook IMG_DEV_WEBHOOK=localhost:5001/redkey-operator-webhook:0.1.0
```

Once the RedKey Operator is available in your local registry, you can follow these steps to deploy it into you K8s cluster:

1. Install the CRD.

```shell
make install
```

2. Create the namespace in which you want you RedKey Operator Webhook to be deployed. By default, `make` uses `redkey-operator-webhook`. Is you want to use a different namespace you should use the `WEBHOOK_NAMESPACE` environment variable to overwritte this value with the one of your choice.

```shell
kubectl create ns redkey-operator-webhook
```

>**The default namespace used by make command is `redkey-operato-webhook`. This value can be overwritten using the `WEBHOOK_NAMESPACE` environment variable.**

3. Generate the manifests, according to the profile you choose to use, to deploy the RedKey Operator. Use the `PROFILE` environment variable to define the profile to use. The manifests are generated in `deployment` directory.

```shell
make process-manifests-webhook PROFILE=debug IMG_DEBUG="localhost:5001/redkey-operator:delve"
```

>**If no `PROFILE` environment variable defined, the default value is `dev`.**

4. Deploy the RedKey Operator Webhook. Uses the `deployment/webhook.yml` generated in the above step.

```shell
make deploy-webhook
```

>**`make` will install the needed tools like `kustomize`, `control-gen` and `envtest` if not available.**

### Debuging RedKey Operator Webhook

If you followed the steps described above to deploy the RedKey Operator Webhook using the `debug` profile you'll have the CRD deployed and a redkey-operator-webhook pod running.

This pod is created using a `golang` image with `Delve` installed on it. This will allow us to easily debug the webhook code following these steps:

1. Build the webhook binary file from the source code.
2. Copy the webhook binary file to the redkey-operator-webhook pod.
3. Run the webhook binary inside the redkey-operator-webhook pod, using Delve to allow remote debugging connections.
4. Port forward `40001` port to be able to connect to the exposed port in the redkey-operator-webhook pod.
5. Connect from the IDE of your choice to remote debugging

To follow the 3 first steps simply execute:

```shell
make debug-webhook
```

The webohok will the be running in your redkey-operator-webhook pod and you'll see pod's standard output printing in your terminal. The webhook logs will then be streamed to the terminal.

To enable the port forwarding:

```shell
make port-forward-webhook
```

You can now attach your local debug from the IDE of your choice to the debug session in the redkey-operator-webhook pod. As an example, you'll find the configuration needed to launch the debug from VSCode here:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Connect to webhook",
            "type": "go",
            "request": "attach",
            "mode": "remote",
            "remotePath": "${workspaceFolder}",
            "port": 40001,
            "host": "127.0.0.1",
            "trace": "verbose",
            "env": {
                "WATCH_NAMESPACE": "default",
                "KUBERNETES_CONFIG":  "${HOME}/.kube/config",
            }
        }
    ]
}
```

Customize the configuration to use your kubeconfig and your namespace if needed.

Before redeploying the webhook code using `make debug-webhook` you can delete the webhook pod using `make delete-operator-webhook`. The webhook pod will be deleted and automatically recreated to suit the replica set requirements using the debugging image.

>**!! Go 1.16 or above is needed to use Delve debugging !!**

## Cleanup

Delete the operator and all associated resources with:

```shell
make delete-rdcl
make undeploy
make uninstall
kubectl delete ns redkey-operator
```

## Deploying a Redis cluster

You can deploy a sample Redis Cluster from `config/samples` folder running:

```shell
make apply-rdcl
```

This will apply the manifest file to create an single node ephemeral RedKeyCluster object with `purgeKeysOnRebalance` set to **true**.

## Tests

Tests can be run with:

```shell
make test
```

## How to test the operator with CRC and operator-sdk locally (OLM deployment)

These commands allows us to deploy with OLM the RedKey Operator in a OC cluster in local environment

### Prerequisites

1. OpenShift 4.x (full installation or CRC)
2. oc command
3. docker or podman commands
4. bash (to access environment variables)
5. operator-sdk command

### Start new CRC cluster

Download the latest release of CRC. https://console.redhat.com/openshift/create/local

Please note the openshift version in your project

Set up the new CRC 

```shell
crc setup
```

Start the new CRC instance:

```shell
crc start
```

### Local Registry

Ensure that the internal image registry is accessible by checking for a route. The following command can be used with OpenShift 4:

```shell
oc get route -n openshift-image-registry
```

If the route is not exposed, the following command can be run:

```shell
oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge
```

We will dynamically create an environment variable with the name of route to the OpenShift registry. The route will have “/openshift” appended as this is a project that all users can access:

```shell
REGISTRY="$(oc get route/default-route -n openshift-image-registry -o=jsonpath='{.spec.host}')/openshift"
```

we need to log in to the OpenShift internal registry

```shell
docker login -u kubeadmin -p $(oc whoami -t) ${REGISTRY} 
```

The output should end with:

```shell
Login Succeeded
```

### OLM Integration Bundle

Export environment variables

```shell
export IMG=${REGISTRY}/redkey-operator:$VERSION // location where your operator image is hosted
export BUNDLE_IMG=${REGISTRY}/redkey-operator-bundle:$VERSION // location where your bundle will be hosted
```

Create a image with the operator

```shell
make docker-build docker-push
```

Create a bundle from the root directory of your project

```shell
make bundle
```

Build and push the bundle image

```shell
make bundle-build bundle-push
```

create a secret inside the namespace where you would like to install the bundle

```shell
kubectl create secret docker-registry regcred --docker-server="${REGISTRY}" --docker-username="kubeadmin" \
    --docker-password=$(oc whoami -t) --dry-run=client -o yaml -n ${namespace} | kubectl apply -f -
```

Install the bundle with OLM

```shell
operator-sdk run bundle $BUNDLE_IMG --skip-tls-verify --pull-secret-name regcred
```
