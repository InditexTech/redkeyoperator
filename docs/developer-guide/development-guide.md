<!--
SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL S.A. (INDITEX S.A.)

SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Development Guide

Quickly provision Redkey cluster environments in Kubernetes.

The operator relies on Redkey cluster functionality to serve client requests.

## Local Development and Testing

A *local K8s cluster* can be used to deploy the Redkey Operator from a pre-built image or by directly compiling from the source code.

This will help develop and test new features, fix bugs, and test releases locally.

As we will see below, you can use the `make` command to deploy the components and an example Redkey Cluster, or run the scripts that automate the basic workflows.

## Create your Kubernetes Cluster

This guide uses Kind, but feel free to use another Kubernetes cluster tool (e.g., Docker Desktop, Rancher Desktop, K3D).

Deploy a k8s cluster with a repository:

``` sh
scripts/kind-with-registry.sh
```

## Redkey Operator

### Deploy Redkey Operator from a custom image

Once your K8s cluster and registry are ready to use, make the image you want to use to deploy the Redkey Operator available. Your cluster must be configured to access your local registry.

> If you want to debug the operator, execute the `export PROFILE=debug` before the following make commands and read [Debugging Redkey Operator](#debugging-redkey-operator) below.

Build and push the Redkey Operator image:

```shell
make docker-build
make docker-push
```

> **To test a released Redkey Operator image**, you'll have to manually pull the image, tag, and push it in the local registry.

Once the Redkey Operator is available in your local registry, deploy it into your K8s cluster:

1. Install the CRD (The `redkey-operator` or  `$NAMESPACE` if defined will be used)

```shell
make install
```

2. Generate the manifests (generated in `deployment` directory) and deploy the Redkey Operator.

```shell
make deploy
```

3. Deploy an example Redkey Cluster from `config/examples` folder. Just so you know, a Redkey Robin image is required; to know how it can be built, see [Redkey Robin](https://github.com/InditexTech/redkeyrobin).

```shell
# Create a rkcl/redis-cluster-ephemeral: 3 nodes, ephemeral: true and purgeKeysOnRebalance: true.
make apply-rkcl
```

### Makefile environment variables

To customize the make commands set the following variables:

| Variable        | Type       | Default                                        | Definition                             |
|-----------------|------------|------------------------------------------------|----------------------------------------|
| `PROFILE`       | dev, debug | dev                                            | determines image and operator deployed |
| `IMG`           | string     | `localhost:5001/redkey-operator:${PROFILE}`    | the operator image  name               |
| `NAMESPACE`     | string     | redkey-operator                                | the namespace to deploy resources in   |
| `PROFILE_ROBIN` | dev, debug | dev                                            | determines robin image and deployed    |
| `IMG_ROBIN`     | string     | `localhost:5001/redkey-robin:${PROFILE_ROBIN}` | the robin image                        |


### Debugging Redkey Operator

If you followed the steps described above to deploy the Redkey Operator using the `debug` profile you'll have the CRD deployed and a redkey-operator pod running.

This pod is created using a `golang` image with `Delve` installed on it. This will allow us to easily debug the manager code following these steps:

#### 1. Prepare the Debug Pod

Execute in a terminal (Use the go version as in .go-version file):

```shell
make debug
```
Actions performed:

- Build the manager binary.
- Copy it to the redkey-operator pod.
- Run the manager binary inside the pod with delve in remote mode.

#### 2. Port Forward

In another terminal forward the `40000` port to connect from the debugger:

```shell
make port-forward
```

#### 3. Connect from your IDE to remote debugging

Attach the IDE/debugger to the debug session. If using VSCode, the configuration could be:

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
            "trace": "verbose"
        }
     ]
}
```


## Cleanup

Delete the operator and all associated resources with:

```shell
make delete-rkcl
make undeploy
make uninstall
kubectl delete ns redkey-operator
```

## Tests

Run unit tests:

```shell
make test
```

End-to-end requires a Kubernetes cluster, such as kind. Run it:

```shell
make test-e2e
```

Or run only an E2E test:

```shell
make test-e2e GINKGO_EXTRA_OPTS='--focus="sets and clears custom labels"'
```

## How to test the operator with CRC and operator-sdk locally (OLM deployment)

These commands allow us to deploy with OLM the Redkey Operator in a OC cluster in local environment

### Prerequisites

1. OpenShift 4.x (full installation or CRC)
2. oc command
3. docker or podman commands
4. bash (to access environment variables)
5. operator-sdk command

### Start new CRC cluster

Download the latest release of CRC. https://console.redhat.com/openshift/create/local

Please note the OpenShift version in your project.

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

We will dynamically create an environment variable with the name of the route to the OpenShift registry. The route will have “/openshift” appended, as this is a project that all users can access:

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
