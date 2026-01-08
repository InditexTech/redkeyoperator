# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Funtional Test for Kubernetes and Openshift Redkey Operator

## Purpose

Funtional test for provision Redkey cluster environments in Kubernetes or Openshift.


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
* `image` name of image that deploy the Redkey Operator 
* `test` name of the desired test `Initialize,ScalingUp,ScalingDown,ChangeStorage,ChangeStorageReplicas,AddLabel,DeleteLabel,InsertData,InsertDataWhileScaling,InsertDataWhileScalingDown,GetSpecificKey,ValidateBasicRedisPrimaryReplica,ScalingUpRedisPrimaryReplica,ScalingDownRedisPrimaryReplica,KillPodRedisPrimaryReplica`
* `newRedisCluster` allows to create a new instance of Redkey Cluster `true,false`
* `typeRedisCluster`  type of deployment of the Redkey Cluster `storage,ephemeral,repprimary`

## Deploying a Redkey cluster

There are 3 commands for creating Redkey clusters. 

* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "storage"` creates a persistent cluster 
* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "ephemeral"` creates an ephemeral cluster. 
* `./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "Initialize" "true" "repprimary"` creates a primary replica cluster. 

Please note that some of the following tooling (such as PoMonitor and load tester) are dependent of the cluster name, so both share the same name.

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
* ValidateBasicRedisPrimaryReplica
* ScalingUpRedisPrimaryReplica
* ScalingDownRedisPrimaryReplica
* KillPodRedisPrimaryReplica


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
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ValidateBasicRedisPrimaryReplica" "true" "repprimary"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ScalingUpRedisPrimaryReplica" "true" "repprimary"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "ScalingDownRedisPrimaryReplica" "true" "repprimary"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.6" "KillPodRedisPrimaryReplica" "true" "repprimary"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "All" "true" "storage"```
 * ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteRedisCluster"```

## Cleanup
Delete the operator and all associated resources with:

* ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteRedisCluster"```
* ```./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.0" "DeleteAll"```
