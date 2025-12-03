#!/bin/bash
#
# Allows to execute differents funtional test for RedkeyCluster deployments.
# Arguments:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4

set -e

namespace=$1
test=$2
newRedisCluster=$3
typeRedisCluster=$4

readonly name="redkey-cluster"

#######################################
# Initialize a new RedkeyCluster validating if exist one installed in the k8 cluster.
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
initializeRedisCluster() {
    REDIS_POD=$(kubectl get po -n $namespace -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    # validate if exists an instance of redkeyCluster installed
    if [ -z "$REDIS_POD" ]; then
        installRedisCluster
    fi
}

#######################################
# Allows to install RedkeyCluster.
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
installRedisCluster() {
    local result=0
    local resultMessage="INFO:: RedkeyCluster created and run correctly"
    rm -f config/samples/kustomization.yaml
    cat <<EOF >config/samples/kustomization.yaml
resources:
 - TYPE_REDISCLUSTER
EOF
    if [[ "$typeRedisCluster" == "storage" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-storage.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "ephemeral" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-ephemeral.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "repmaster" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-repmaster.yaml|g" "config/samples/kustomization.yaml"
    fi

    # build and apply cluster in the cluster
    make dev-apply-rkcl

    echo 'INFO::waiting for Initializing status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Initializing 20
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR::Failure: Script failed"
        result=1
    else
        echo 'INFO::waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure: Script failed"
            result=1
        fi
    fi
    echo $resultMessage
    kubectl get all,rkcl -n $namespace
    if [[ "$result" != "0" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale up RedkeyCluster.
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
scaleUpCluster() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: Scale cluster with scale --replicas=6'
    kubectl scale rkcl -n $namespace $name --replicas=6
    echo 'INFO::waiting for ScalingUp status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR::Failure: Script failed"
        result=1
    else
        echo 'INFO::waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure: Script failed"
            result=1
        fi
    fi
    echo '##########################################'
    kubectl get all,rkcl -n $namespace
    echo $resultMessage
    exit $result
}

#######################################
# Test for Scale Down RedkeyCluster.
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
scaleDownCluster() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: Scale cluster with scale --replicas=6'
    kubectl scale rkcl -n $namespace $name --replicas=6
    echo 'INFO:: waiting for ScalingUp status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 180
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Failure: Script failed"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rkcl -n $namespace
            echo '##########################################'
            echo 'INFO:: Scale down cluster with scale --replicas=3'
            kubectl scale rkcl -n $namespace $name --replicas=3
            echo 'INFO:: waiting for ScalingDown status in redkeyCluster'
            ./test/cmd/waitforstatus.sh $namespace $name ScalingDown 180
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            else
                echo '##########################################'
                echo 'INFO:: waiting for Ready status in redkeyCluster'
                ./test/cmd/waitforstatus.sh $namespace $name Ready 540
                result=$?
                if [[ "$result" != "0" ]]; then
                    resultMessage="Failure: Script failed"
                    result=1
                fi
            fi
        fi
    fi
    kubectl get all,rkcl -n $namespace
    echo $resultMessage
    exit $result
}



#######################################
# Allow to validate if RedkeyCluster primary-replica works properly
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
validateRedisMasterSlave() {
    local minReplicas=3
    local minReplicasPerMaster=1
    local minRepStatefulSet
    local nodes
    local totalMasters=0
    local totalSlaves=0
    local resultMessage="0"
    # get replicas for RedkeyCluster
    local rkclReplicas=$(kubectl -n $namespace get rkcl $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas per primary for RedkeyCluster
    local rkclReplicasPerMaster=$(kubectl -n $namespace get rkcl $name -o custom-columns=':spec.replicasPerMaster' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas for statefulset associate to RedkeyCluster
    local stsReplicas=$(kubectl -n $namespace get sts $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get minimum replicas calculate for StateFulSet
    minRkclRepSlaves=$((rkclReplicas * rkclReplicasPerMaster))
    minRepStatefulSet=$((rkclReplicas + minRkclRepSlaves))

    # validate if rediscluster has the minimum replicas
    if [[ $rkclReplicas -lt $minReplicas ]]; then
        resultMessage="ERROR:: Minimum configuration required RedisCluster minReplicas"
    # validate if rediscluster has the minimum replicas per primary
    elif [[ $rkclReplicasPerMaster -lt $minReplicasPerMaster ]]; then
        resultMessage="ERROR:: Minimum configuration required RedisCluster minReplicasPerMaster"
    # validate if statefulset creates the minimum replicas per replicas per primary (redisCluster.spec.replicas + (redisCluster.spec.replicas*replicasPerMaster))
    elif [[ $minRepStatefulSet -lt $stsReplicas ]]; then
        resultMessage="ERROR:: Minimum configuration required StateFulSet minRepStatefulSet"
    else
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
        for POD in $REDIS_POD; do
            # get and parse the info about nodes configured in the Cluster
            nodes=$(kubectl -n $namespace exec -i $POD -- redis-cli CLUSTER NODES | tr " " "&" | tr "\r" ";")
            break
        done
        # Convert to array the nodes info
        nodeList="$(echo $nodes)"
        IFS=";" read -a nodeArray <<<$nodeList
        for node in ${nodeArray[@]}; do
            set -f                 # avoid globbing (expansion of *).
            array=(${node//"&"/ }) # convert to array the specific info about the node
            podType="${array[2]}"  # get the position that have the info about the pod type
            # validate if the pod is primary
            if [[ "$podType" == *"master"* ]]; then
                ((totalMasters = totalMasters + 1))
            fi
            # validate if the pod is slave
            if [[ "$podType" == *"slave"* ]]; then
                ((totalSlaves = totalSlaves + 1))
            fi
        done
        # validate if exists the minimum pods primary configured
        if (($rkclReplicas != $totalMasters)); then
            resultMessage="ERROR:: Minimum configuration required - pods primaries"
        fi
        # validate if exists the minimum pods slave configured
        if (($minRkclRepSlaves != $totalSlaves)); then
            resultMessage="ERROR:: Minimum configuration required - pods slaves"
        fi
    fi
    echo $resultMessage
}

#######################################
# Allow to validate if  the basic configuration of RedkeyCluster
# primary-replica works properly
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
validateBasicRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: RedkeyCluster configured correctly for Redis primary replica configuration"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo "INFO:: Validating basic configuration in Cluster with primary-replica configuration"
    message=$(validateRedisMasterSlave)
    if [[ "$message" != "0" ]]; then
        resultMessage=$message
        result=1
    else
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
        for POD in $REDIS_POD; do
            # get and parse the info about nodes configured in the Cluster
            kubectl -n $namespace exec -i $POD -- redis-cli CLUSTER NODES
            echo '##########################################'
            break
        done
    fi
    echo $resultMessage
    exit $result
}

echo "##### NAMESPACE=$namespace, NAME=$name, IMAGE=$image TEST=$test NEW_REDISCLUSTER=$newRedisCluster TYPE_REDISCLUSTER=$typeRedisCluster #####"

case "${test}" in
Initialize)
    initializeRedisCluster
    ;;
ScalingUp)
    scaleUpCluster
    ;;
ScalingDown)
    scaleDownCluster
    ;;
ValidateBasicRedisMasterSlave)
    validateBasicRedisMasterSlave
    ;;
*)
    echo "please select a correct test"
    ;;
esac

# ./test/cmd/redisClusterTest.sh "default" "Initialize" "false" "false"
# ./test/cmd/redisClusterTest.sh test-${{ github.event.pull_request.head.sha }} "Initialize" "false" "false"
# ./test/cmd/redisClusterTest.sh "redis-system" "ScalingUp" "false" "ephemeral"
# ./test/cmd/redisClusterTest.sh "redis-system" "ScalingDown" "false" "ephemeral"
# ./test/cmd/redisClusterTest.sh "redis-system" "ValidateBasicRedisMasterSlave" "false" "repmaster"
