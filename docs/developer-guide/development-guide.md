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

Deploy a k8s cluster with a trysted repository. You can use the following proposed script (from Kind website) or use the kind command (see the [official Kind Quick Start](https://kind.sigs.k8s.io/docs/user/quick-start/) for detailed usage guide):

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

### Chaos tests

Chaos tests validate operator resilience under disruptive conditions: random pod
deletions, scaling, operator restarts, and topology corruption — all while the
cluster is under k6 write/read load. They live in `test/chaos/` and run via:

```shell
make test-chaos
```

Run a single scenario:

```shell
make test-chaos GINKGO_EXTRA_OPTS='--focus="survives continuous scaling"'
```

#### Chaos test environment variables

The chaos suite runs multiple Ginkgo processes in parallel, each creating its
own isolated namespace with an operator, Robin, and a Redis cluster. The
following variables control test behavior and should be set in your shell or
`.envrc` before running `make test-chaos`:

```shell
export TEST_PARALLEL_PROCESS=8
export GOMAXPROCS=8
export CHAOS_KEEP_NAMESPACE_ON_FAILED=true
export IMG_ROBIN=localhost:5001/redkey-robin:0.1.0
```

| Variable                        | Default                          | Description                                                                                                                                                                                                                                                                                   |
|---------------------------------|----------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `TEST_PARALLEL_PROCESS`         | `8`                              | Number of parallel Ginkgo processes (`-procs`). Each process runs a separate test spec in its own namespace. Higher values run more specs concurrently but require more cluster resources. This also applies to E2E tests.                                                                     |
| `GOMAXPROCS`                    | `8`                              | Go runtime parallelism. Should match `TEST_PARALLEL_PROCESS` so each Ginkgo process has a dedicated OS thread. Setting this lower than `TEST_PARALLEL_PROCESS` causes goroutine contention; setting it higher wastes CPU without benefit.                                                      |
| `CHAOS_ITERATIONS`              | `10`                             | Number of chaos loop iterations per test spec. Each iteration performs disruptive actions (scale, delete pods, etc.) and waits for recovery. More iterations increase coverage but extend the total run time proportionally.                                                                    |
| `CHAOS_TIMEOUT`                 | `100m`                           | Maximum wall-clock time Ginkgo allows for the entire chaos suite (`--timeout`). Must be large enough to accommodate `CHAOS_ITERATIONS` x recovery time x number of specs / `TEST_PARALLEL_PROCESS`. With 10 iterations and 8 parallel processes, 100 minutes is typically sufficient.          |
| `CHAOS_SEED`                    | *(auto: Ginkgo random seed)*     | Fixed random seed for reproducibility. When a chaos run fails, the seed is printed in the output so you can replay the exact sequence of random actions.                                                                                                                                      |
| `CHAOS_KEEP_NAMESPACE_ON_FAILED`| *(unset)*                        | When set to any non-empty value, failed test namespaces are preserved instead of deleted. This allows post-mortem inspection of pods, logs, and cluster state with `kubectl`. Remember to clean up namespaces manually afterwards.                                                             |
| `IMG_ROBIN`                     | `ghcr.io/inditextech/redkey-robin:$(ROBIN_VERSION)` | Robin sidecar image. For local development, point this to your local registry (e.g. `localhost:5001/redkey-robin:0.1.0`). Passed to tests as `ROBIN_IMAGE` via `GINKGO_ENV`.                                                                                              |
| `K6_IMG`                        | `localhost:5001/redkey-k6:dev`   | k6 load generator image (built with xk6-redis extension). Build it with `make k6-build` before running chaos tests.                                                                                                                                                                          |

#### Relationship between parallelism and timeouts

`TEST_PARALLEL_PROCESS` controls how many test specs run concurrently. The chaos
suite has 8 specs (4 scenarios x 2 `purgeKeysOnRebalance` modes), so with
`TEST_PARALLEL_PROCESS=8` all specs run in parallel and the total wall-clock
time equals roughly the duration of the slowest single spec.

`CHAOS_TIMEOUT` must account for the worst case: if one spec takes longer than
expected (e.g. slow recovery after a scale-down), the timeout must be generous
enough to avoid killing a spec mid-recovery. As a rule of thumb:

- With `CHAOS_ITERATIONS=10` and `TEST_PARALLEL_PROCESS=8`: `CHAOS_TIMEOUT=100m`
- With `CHAOS_ITERATIONS=5` and `TEST_PARALLEL_PROCESS=4`: `CHAOS_TIMEOUT=60m`

`GOMAXPROCS` should always match `TEST_PARALLEL_PROCESS`. Each Ginkgo process
creates its own Kubernetes clients with independent rate limiters (QPS=5,
Burst=10), so they don't contend on API access — but they do share CPU.

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
