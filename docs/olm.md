<!--
SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL S.A. (INDITEX S.A.)

SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Test Operator deployment in Kubernetes using OLM

## Installing Operator Lifecycle Manager (OLM)

If you are not using OpenShift, you can install OLM in your Kubernetes cluster by following the following instructions.

We assume you already have a Kubernetes cluster up and running, and this cluster is currently selected as your `current-context` vie `kubectl`. If you don't have one, you can create a local cluster using tools like Minikube or Kind.

```bash
kind create cluster
```

Operator Lifecycle Manager (OLM) can be installed using either `kubectl` or `operator-sdk`.

### Install the Operator Lyfecycle Manager (OLM) using `operator-sdk`

```bash
operator-sdk olm install
```

Verify that OLM is installed correctly:

```bash
operator-sdk olm status
```

This command should show you the status of OLM components, including the `catalog-operator`, `olm-operator`, and `packageserver`.

```bash
INFO[0000] Fetching CRDs for version "v0.28.0"          
INFO[0000] Fetching resources for resolved version "v0.28.0" 
INFO[0000] Successfully got OLM status for version "v0.28.0" 

NAME                                            NAMESPACE    KIND                        STATUS
global-operators                                operators    OperatorGroup               Installed
olm                                                          Namespace                   Installed
operatorconditions.operators.coreos.com                      CustomResourceDefinition    Installed
catalog-operator                                olm          Deployment                  Installed
olm-operator-binding-olm                                     ClusterRoleBinding          Installed
olmconfigs.operators.coreos.com                              CustomResourceDefinition    Installed
system:controller:operator-lifecycle-manager                 ClusterRole                 Installed
clusterserviceversions.operators.coreos.com                  CustomResourceDefinition    Installed
packageserver                                   olm          ClusterServiceVersion       Installed
operatorgroups.operators.coreos.com                          CustomResourceDefinition    Installed
aggregate-olm-view                                           ClusterRole                 Installed
cluster                                                      OLMConfig                   Installed
aggregate-olm-edit                                           ClusterRole                 Installed
subscriptions.operators.coreos.com                           CustomResourceDefinition    Installed
operators.operators.coreos.com                               CustomResourceDefinition    Installed
olm-operator                                    olm          Deployment                  Installed
installplans.operators.coreos.com                            CustomResourceDefinition    Installed
operatorhubio-catalog                           olm          CatalogSource               Installed
olm-operators                                   olm          OperatorGroup               Installed
operators                                                    Namespace                   Installed
catalogsources.operators.coreos.com                          CustomResourceDefinition    Installed
olm-operator-serviceaccount                     olm          ServiceAccount              Installed
```

### Install the Operator Lyfecycle Manager (OLM) using `kubectl`

```bash
kubectl apply -f https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v0.40.0/crds.yaml
kubectl apply -f https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v0.40.0/olm.yaml
```

Verify that OLM is installed correctly:

```bash
kubectl get pods -n olm
```

You should see the following output, indicating that the OLM components are running:

```bash
NAME                               READY   STATUS    RESTARTS   AGE
catalog-operator-9f6dc8c87-v9ljl   1/1     Running   0          12m
olm-operator-6bccddc987-xz7cv      1/1     Running   0          12m
operatorhubio-catalog-lmcsk        1/1     Running   0          12m
packageserver-7899cbcfc6-gkc2j     1/1     Running   0          12m
packageserver-7899cbcfc6-hpxzw     1/1     Running   0          12m
```

## Adding the catalog containing the operator to OLM

To add the catalog containing the operator to OLM, you need to create a `CatalogSource` resource. This resource defines the source of the operator's catalog, which can be a container image, a local directory, or a gRPC server.

In this example, we will create a `CatalogSource` that points to a container image hosted on ghcr.io (change the version tag as needed). The image should contain the operator's catalog in the format expected by OLM.

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: redkey-catalog
  namespace: olm
spec:
  displayName: Redkey Operator Catalog
  publisher: InditexTech
  sourceType: grpc
  image: ghcr.io/inditextech/redkey-operator-catalog:v0.1.0
```

Apply the `CatalogSource` resource to your cluster:

```bash
kubectl apply -f catalogSource.yaml
```

Verify that the catalog is added correctly:

```bash
$ kubectl get catalogsource -n olm
NAME                               READY   STATUS    RESTARTS   AGE
redkey-catalog-vvcnj               1/1     Running   0          14s
[...]
```

Verify the health of the catalog:

```bash
kubectl get catalogsource redkey-catalog -n olm -o yaml
```

## Check the available operators in the catalog

You can inspect the loaded `packagemanifests` list to check the available operators in the catalog:

```bash
$ kubectl get packagemanifests -n olm | grep redkey-operator
redkey-operator                                                   4m37s
```

## Create an OperatorGroup

The namespaces where the operator will be installed must be defined in an `OperatorGroup` resource. This resource defines the scope of the operator, which can be a single namespace, multiple namespaces, or the entire cluster.

Create an `OperatorGroup` resource that targets the `default` namespace:

```
apiVersion: operators.coreos.com/v1alpha2
kind: OperatorGroup
metadata:
  name: redkey-operatorgroup
  namespace: default
spec:
  targetNamespaces:
  - default
```

Create a file named `operatorGroup.yaml` with the above content and apply it to your cluster:

```bash
kubectl apply -f operatorGroup.yaml
```

## Create a Subscription to the operator

Finally, you need to create a `Subscription` resource that defines the operator you want to install, the channel you want to subscribe to, and the source of the catalog.

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: redkey-operator-subscription
  namespace: default
spec:
  channel: alpha
  name: redkey-operator
  startingCSV: redkey-operator.v0.1.0
  source: redkey-catalog
  sourceNamespace: olm
```

Create a file named `subscription.yaml` with the above content and apply it to your cluster:

```bash
kubectl apply -f subscription.yaml
```

An `InstallPlan` will be automatically created and executed to install the operator. You can check the status of the `InstallPlan` to see if the operator is being installed correctly:

```bash
kubectl get installplan -n default
```

Then, you can check the status of the `ClusterServiceVersion` (CSV) to see if the operator is running:

```bash
$ kubectl get clusterserviceversion -n default -w
NAME                     DISPLAY           VERSION   REPLACES   PHASE
redkey-operator.v0.1.0   Redkey Operator   0.1.0                
redkey-operator.v0.1.0   Redkey Operator   0.1.0                
redkey-operator.v0.1.0   Redkey Operator   0.1.0                
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Pending
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Pending
redkey-operator.v0.1.0   Redkey Operator   0.1.0                InstallReady
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Installing
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Installing
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Installing
redkey-operator.v0.1.0   Redkey Operator   0.1.0                Succeeded
```

And the Operator should be up and running in the `default` namespace:

```bash
$ kubectl get pods -n default | grep redkey-operator
NAME                               READY   STATUS    RESTARTS   AGE
redkey-operator-749595567c-qdq4g   1/1     Running   0          72s
```

## Deploy a Redkey Cluster using the Operator

Save the following content in a file named `rkcl.yaml`:

```yaml
apiVersion: redkey.inditex.dev/v1
kind: RedkeyCluster
metadata:
  labels:
    app.kubernetes.io/name: redkey-cluster-operator
    app.kubernetes.io/managed-by: kustomize
  name: redis-cluster-ephemeral
spec:
  primaries: 3
  ephemeral: true
  accessModes: 
  - ReadWriteOnce
  image: redis:8-bookworm
  purgeKeysOnRebalance: true
  labels:
    team: a-team
    custom: custom-label
  config: |
    maxmemory 90mb
    maxmemory-samples 5
    maxmemory-policy allkeys-lru
    protected-mode no
    appendonly no
    save ""
  resources:
    limits:
      cpu: 100m
      memory: 128Mi
  robin:
    config:
      reconciler:
        intervalSeconds: 30
        operationCleanUpIntervalSeconds: 30
      cluster:
        healthProbePeriodSeconds: 60
        healingTimeSeconds: 60
        maxRetries: 10
        backOff: 10
      metrics:
        intervalSeconds: 60
        redisInfoKeys:
          - keyspace_hits
          - evicted_keys
          - connected_clients
          - total_commands_processed
          - keyspace_misses
          - expired_keys
          - redis_version
          - used_memory_rss
          - maxmemory
          - used_cpu_sys
          - used_cpu_sys_children
          - used_cpu_user
          - used_cpu_user_children
          - total_net_input_bytes
          - total_net_output_bytes
          - aof_base_size
          - aof_current_size
          - mem_aof_buffer
    template:
      spec:
        containers:
          - image: ghcr.io/inditextech/redkey-robin:0.1.0
            name: robin
            imagePullPolicy: Always
            ports:
              - containerPort: 8080
                name: prometheus
                protocol: TCP
            volumeMounts:
              - mountPath: /opt/conf/configmap
                name: redis-cluster-ephemeral-robin-config
            resources:
              requests:
                cpu: 500m
                memory: 100Mi
              limits:
                cpu: 1
                memory: 200Mi
        volumes:
          - configMap:
              defaultMode: 420
              name: redis-cluster-ephemeral-robin
            name: redis-cluster-ephemeral-robin-config
```

Apply the `RedkeyCluster` resource to your cluster:

```bash
kubectl apply -f rkcl.yaml
```

You can check the status of the `RedkeyCluster` to see if it is being created correctly:

```bash
$ kubectl get rkcl -o wide -w
NAME                      PRIMARIES   REPLICAS   EPHEMERAL   PURGEKEYS   IMAGE              STORAGE   STORAGECLASSNAME   DELETEPVC   STATUS   SUBSTATUS   PARTITION
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false                            
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false                            
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false                            
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false       Initializing               
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false       Configuring                
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false       Configuring                
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false       Configuring                
redis-cluster-ephemeral   3           0          true        true        redis:8-bookworm                                false       Ready
```
