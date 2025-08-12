# Funtional Test for Kubernetes and Openshift RedKey Operator

## Purpose

Funtional test for provision Redis cluster environments in Kubernetes or Openshift.


## Folder structure

The `tests` folder contains all necessary scripts and manifests

Manifests:
* `./test/cmd/redisClusterTest.sh` - main script with differents test
* `./test/cmd/waitforstatus.sh` - script that complements the tests

## How to run the test

### Prerequisites
1. make
2. kustomize - run `make kustomize` to install local kustomize version
3. kubectl with configured access to create CRD, namespaces, deployments
4. If on macos - install linux compatible utils - `brew install coreutils`, as makefile uses linux version of sed

## Variables

```
* namespace=$1
* image=$2
* test=$3
* newRedisCluster=$4
* typeRedisCluster=$5
```

* `namespace` name of namespace where the funtional will be executed
* `image` name of image that deploy the RedKey Operator 
* `test` name of the desired test `Initialize,ScalingUp,ScalingDown,ChangeStorage,ChangeStorageReplicas,AddLabel,DeleteLabel,InsertData,InsertDataWhileScaling,InsertDataWhileScalingDown,GetSpecificKey,ValidateBasicRedisMasterSlave,ScalingUpRedisMasterSlave,ScalingDownRedisMasterSlave,KillPodRedisMasterSlave`
* `newRedisCluster` allows to create a new instance of Redis Cluster `true,false`
* `typeRedisCluster`  type of deployment of the Redis Cluster `storage,ephemeral,repmaster`

## Deploying a Redis cluster

There are 3 commands for creating Redis clusters. 

* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "storage"` creates a persistent redis cluster 
* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "ephemeral"` creates an ephemeral redis cluster. 
* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "repmaster"` creates a master slave redis cluster. 

Please note that some of the following tooling (such as PoMonitor and load tester) are dependent of the Redis cluster name, so both share the same name.

## Available Test

* Initialize
* ScalingUp
* ScalingDown
* ChangeStorage
* ChangeStorageReplicas
* AddLabel
* DeleteLabel
* InsertData
* InsertDataWhileScaling
* InsertDataWhileScalingDown
* GetSpecificKey
* ValidateBasicRedisMasterSlave
* ScalingUpRedisMasterSlave
* ScalingDownRedisMasterSlave
* KillPodRedisMasterSlave


Tests can be run with (examples):


 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "storage" ```
 * ```./test/cmd/redisClusterTest.sh test-${{ github.event.pull_request.head.sha }} ${{env.JFROG_SNAPSHOT_REGISTRY}}/redkey-operator:sha-${{ github.event.pull_request. head.sha }} "Initialize" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "ScalingUp" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "ScalingDown" "true" "storage" ```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "ChangeStorage" "true" "storage" ```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "ChangeStorageReplicas" "false" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "AddLabel" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.1" "DeleteLabel" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "ChangeLabels" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.1" "InsertData" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.1" "InsertDataWhileScaling" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.1" "InsertDataWhileScalingDown" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.1" "GetSpecificKey" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ValidateBasicRedisMasterSlave" "true" "repmaster"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ScalingUpRedisMasterSlave" "true" "repmaster"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ScalingDownRedisMasterSlave" "true" "repmaster"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "KillPodRedisMasterSlave" "true" "repmaster"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "All" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteRedisCluster"```

## Cleanup
Delete the operator and all associated resources with:

* ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteRedisCluster"```
* ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteAll"```
