# Developer Guide

Quickly provision Redis cluster environments in Kubernetes or Openshift.

The operator relies on Redis cluster functionality to serve client requests.

## Local development and testing

A *local K8s cluster* can be used to deploy the Redis Operator from a pre-built image or directly compiling from the source code.

This will be helpfull to develop and test new features, fix bugs, and test releases locally.

As we will see below, you can use the `make` command to deploy the different components, as well as to deploy a sample Redis Cluster, or use the scripts that automate the basic workflows.

## Development profiles

Three **Deployment Profiles** have been defined. Basically, these profiles determine which image will be used to deploy the Redis Operator pod. These are the 3 Deployment Profiles and the default images used to create the Redis Operator pod:

| Profile | Image used to create the Redis Operator pod | Purpose |
|---------|---------------------------------------------|---------|
| debug | inditex-docker-snapshot.jfrog.io/production/itxapps/delve:1.24.0 | Debug code from your IDE using Delve |
| dev | redis-operator:0.1.0 | Test a locally built (from source code) release | 
| pro | axinregistry1.central.inditex.grp/itxapps/operator.redis-operator:1.2.1 | Test a released version |

## Create your Kubernetes cluster

You can deploy your own Kubernetes cluster locally to develop Redis Operator with the tool of your choice (Docker Desktop, Rancher Desktop, K3D, Kind,...).

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

## Deploy Redis Operator from a custom image

Once your K8s cluster and registry are ready to work with, you need to make available the image you want to use to deploy Redis Operator.

Your cluster must be configured to be able to access your local registry.

We provide and easy way to build and push the images for `dev` and `debug` profiles:

- `make dev-docker-build`: builds an image containing the Redis Operator manager built from the source code. This image is published in Docker local registry.
- `make dev-docker-push`: pushes the image built with the command above to the corresponding registry.
- `make debug-docker-build`: builds an image that will allow us to create an *empty* pod as the redis operator to which we will copy the manager binary and run it, as we'll explain later.
- `make debug-docker-push`: pushes the image built with the command above to he corresponding registry.

**To test a released Redis Operator version you'll have to manually pull the image, tag and push to your local registry.**

The image names used by default by each profile (shown in the table above) can be overwritten using the environment variables:

- `IMG`: overwrittes the image name when using `pro` profile.
- `IMG_DEV`: overwrittes the image name when using `dev` profile.
- `IMG_DEBUG`: overwrittes the image name when using `debug` profile.

E.G. these are the commands to build and push the image for `debug` profile:

```
make debug-docker-build IMG_DEBUG=localhost:5001/redis-operator:delve
make debug-docker-push IMG_DEBUG=localhost:5001/redis-operator:delve
```

E.G. the commands when using `dev` profile:

```
make debug-docker-build IMG_DEV=localhost:5001/redis-operator:0.1.0
make debug-docker-push IMG_DEV=localhost:5001/redis-operator:0.1.0
```

Once the Redis Operator is available in your local registry, you can follow these steps to deploy it into you K8s cluster:

1. Intall the CRD.

```
make install
```

2. Create the namespace in which you want you Redis Operator to be deployed. By default, `make` uses `redis-operator`. Is you want to use a different namespace you should use the `NAMESPACE` environment variable to overwritte this value with the one of your choice.

```
kubectl create ns redis-operator
```

>**The default namespace used by make command is `redis-operator`. This value can be overwritten using the `NAMESPACE` environment variable.**

3. Generate the manifests, according to the profile you choose to use, to deploy the Redis Operator. Use the `PROFILE` environment variable to define the profile to use. The manifests are generated in `deployment` directory.

```
make process-manifests PROFILE=debug IMG_DEBUG="localhost:5001/redis-operator:delve"
```

>**If no `PROFILE` environment variable defined, the default value is `dev`.**

4. Deploy the Redis Operator. Uses the `deployment/deployment.yml` generated in the above step.

```
make deploy
```

>**`make` will install the needed tools like `kustomize`, `control-gen` and `envtest` if not available.**

## Debuging Redis Operator

If you followed the steps described above to deploy the Redis Operator using the `debug` profile you'll have the CRD deployed and a redis-operator pod running.

This pod is created using a `golang` image with `Delve` installed on it. This will allow us to easily debug the manager code following these steps:

1. Build the manager binary file from the source code.
2. Copy the manager binary file to the redis-operator pod.
3. Run the manager binary inside the redis-operator pod, using Delve to allow remote debugging connections.
4. Port forward `40000` port to be able to connect to the exposed port in the redis-operator pod.
5. Connect from the IDE of your choice to remote debugging

To follow the 3 first steps simply execute:

```
make debug
```

The manager will the be running in your redis-operator pod and you'll see pod's standard output printing in your terminal. The operator logs will then be streamed to the terminal.

To enable the port forwarding:

```
make port-foward
```

You can now attach your local debug from the IDE of your choice to the debug session in the redis-operator pod. As an example, you'll find the configuration needed to launch the debug from VSCode here:

```
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

## Cleanup

Delete the operator and all associated resources with:

```
make dev-delete-rdcl
make undeploy
make uninstall
kubectl delete ns redis-operator
```

## Deploying a Redis cluster

There are two sample manifests for creating Redis clusters under the folder `config/samples`. `redis_v1alpha1_rediscluster.yaml` creates a persistent cluster and `redis_v1alpha1_rediscluster_ephemeral.yaml` creates an ephemeral cluster. Please note that some of the following tooling (such as PoMonitor and load tester) are dependent of the Redis cluster name, so both samples share the same name.

### Deploying a master-replica Redis cluster

Create an ephemeral cluster `redis_v1alpha1_rediscluster_ephemeral.yaml` **IMPORTANT specify at least `replicas: 3` `replicasPerMaster: 1`** this is important to promote failover and renconcile cluster when master fail

## Getting the metrics from Redis Cluster and the Operator

The folder `_local_tools/tooling` contains manifests for **Prometheus Operator** and a **Prometheus Instance**, a **Grafana Instance**, two **PodMonitor** instances and an **InfluxDB Instance**. These tools can be applied by running
```
kustomize build _local_tools/tooling | kubectl apply -f -
```
The operator serves its metrics via the port 8080. One of the PodMonitors scrapes metrics out of Operator instance and writes them to the Prometheus instance. 
The other PodMonitor uses a label match to find out all Redis instances and scrapes all the Redis metrics and copies to the Prometheus instance.

By default, Redis pods does not expose a metrics port. To achieve this, a sidecar should be added to each pod. This operation requires a change on the code where the Redis cluster statefulset is created. The can can be applied by running
```
git apply _local_tools/monitoring/redis_monitoring_sidecar_apply.diff
```
and can be removed by running
```
git apply _local_tools/monitoring/redis_monitoring_sidecar_remove.diff
```

---
### !!! WARNING !!!
> It's crucial to remove this path before commiting the code to the repository as this sidecar approach is just for development purposes and not for production.

---

After applying the patch and running the operator by running `make dev-deploy`, newly created Redis clusters will include this metrics sidecar on each Redis node. By this time all the metrics should be on the Prometheus instance and ready to inspect.

## Visualizing the metrics

Grafana service's type is ClusterIP and the exposed port should be forwarded in order to reach the service from the local development machine.

```
kubectl port-forward service/grafana 28080:80
```

After forwarding the port, Grafana interface can be reached by accessing `http://localhost:28080` from a browser. Grafana username and password can be found inside the `grafana.yml` file. 
The data source for all metrics can be found by adding a Data Source by the type Prometheus and with the address `http://prometheus-operated:9090`. A sample dashboard can be created by importing the `_local_tools/dashboards/redis-prometheus.json` file.

![img.png](../images/redis-dashboard.png)

## How to run a load test

The folder `_local_tools/tooling` contains the file `redis-load-tester.yml` which can be used to deploy a load tester.

This load tester randomly generates keys and writes them on the redis cluster. Load tester also writes the metrics about its load to the InfluxDB instance.

The Influxdb can be added as a data source on Grafana by using `http://inflluxdb:8086` as the address and `myk6db` as the database name. 

The dashboard can be added by importing `_local_tools/dashboards/redis-k6.json` file.

![img.png](../images/redis-load-dashboard.png)

## Tests

There are some basic tests validating deployments.
Tests can be run with:
```
make test
```

## How to test the operator with CRC and operator-sdk locally (OLM deployment)

These commands allows us to deploy with OLM the Redis Operator in a OC cluster in local environment

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

```
crc setup
```

Start the new CRC instance:

```
crc start
```

### Local Registry

Ensure that the internal image registry is accessible by checking for a route. The following command can be used with OpenShift 4:

```
oc get route -n openshift-image-registry
```

If the route is not exposed, the following command can be run:

```
oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge
```

We will dynamically create an environment variable with the name of route to the OpenShift registry. The route will have “/openshift” appended as this is a project that all users can access:

```
REGISTRY="$(oc get route/default-route -n openshift-image-registry -o=jsonpath='{.spec.host}')/openshift"
```

we need to log in to the OpenShift internal registry

```
docker login -u kubeadmin -p $(oc whoami -t) ${REGISTRY} 
```

The output should end with:

```
Login Succeeded
```

### OLM Integration Bundle

Export environment variables

```
export IMG=${REGISTRY}/redis-operator:$VERSION // location where your operator image is hosted
export BUNDLE_IMG=${REGISTRY}/redis-operator-bundle:$VERSION // location where your bundle will be hosted
```

Create a image with the operator

```
make docker-build docker-push
```

Create a bundle from the root directory of your project

```
make bundle
```

Build and push the bundle image

```
make bundle-build bundle-push
```

create a secret inside the namespace where you would like to install the bundle

```
kubectl create secret docker-registry regcred --docker-server="${REGISTRY}" --docker-username="kubeadmin" \
    --docker-password=$(oc whoami -t) --dry-run=client -o yaml -n ${namespace} | kubectl apply -f -
```


Install the bundle with OLM

```
operator-sdk run bundle $BUNDLE_IMG --skip-tls-verify --pull-secret-name regcred
```
