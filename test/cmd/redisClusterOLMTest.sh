#!/bin/bash
#
# Allows to execute differents funtional test for RedisCluster deployments.
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

readonly name="redis-cluster"

#######################################
# Initialize a new RedisCluster validating if exist one installed in the k8 cluster.
# Globals:
#   namespace=$1
#   test=$2
#   newRedisCluster=$3
#   typeRedisCluster=$4
# Arguments:
#   None
#######################################
initializeRedisCluster() {
    REDIS_POD=$(kubectl get po -n $namespace -l='redis.rediscluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    # validate if exists an instance of redisCluster installed
    if [ -z "$REDIS_POD" ]; then
        installRedisCluster
    fi
}

#######################################
# Allows to install RedisCluster.
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
    local resultMessage="INFO:: RedisCluster created and run correctly"
    rm -f config/samples/kustomization.yaml
    cat <<EOF >config/samples/kustomization.yaml
resources:
 - TYPE_REDISCLUSTER
EOF
    if [[ "$typeRedisCluster" == "storage" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "ephemeral" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-ephemeral.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "repmaster" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-repmaster.yaml|g" "config/samples/kustomization.yaml"
    fi

    # build and apply redis cluster in the cluster
    make dev-apply-rdcl

    echo 'INFO::waiting for Initializing status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Initializing 20
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR::Failure: Script failed"
        result=1
    else
        echo 'INFO::waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure: Script failed"
            result=1
        fi
    fi
    echo $resultMessage
    kubectl get all,rdcl -n $namespace
    if [[ "$result" != "0" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale up RedisCluster.
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
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: Scale cluster with scale --replicas=6'
    kubectl scale rdcl -n $namespace $name --replicas=6
    echo 'INFO::waiting for ScalingUp status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR::Failure: Script failed"
        result=1
    else
        echo 'INFO::waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure: Script failed"
            result=1
        fi
    fi
    echo '##########################################'
    kubectl get all,rdcl -n $namespace
    echo $resultMessage
    exit $result
}

#######################################
# Test for Scale Down RedisCluster.
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
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: Scale cluster with scale --replicas=6'
    kubectl scale rdcl -n $namespace $name --replicas=6
    echo 'INFO:: waiting for ScalingUp status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 180
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Failure: Script failed"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rdcl -n $namespace
            echo '##########################################'
            echo 'INFO:: Scale down cluster with scale --replicas=3'
            kubectl scale rdcl -n $namespace $name --replicas=3
            echo 'INFO:: waiting for ScalingDown status in redisCluster'
            ./test/cmd/waitforstatus.sh $namespace $name ScalingDown 180
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            else
                echo '##########################################'
                echo 'INFO:: waiting for Ready status in redisCluster'
                ./test/cmd/waitforstatus.sh $namespace $name Ready 540
                result=$?
                if [[ "$result" != "0" ]]; then
                    resultMessage="Failure: Script failed"
                    result=1
                fi
            fi
        fi
    fi
    kubectl get all,rdcl -n $namespace
    echo $resultMessage
    exit $result
}



#######################################
# Allow to validate if RedisCluster master-slave works properly
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
    # get replicas for RedisCluster
    local rdclReplicas=$(kubectl -n $namespace get rdcl $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas per master for RedisCluster
    local rdclReplicasPerMaster=$(kubectl -n $namespace get rdcl $name -o custom-columns=':spec.replicasPerMaster' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas for statefulset associate to RedisCluster
    local stsReplicas=$(kubectl -n $namespace get sts $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get minimum replicas calculate for StateFulSet
    minRdclRepSlaves=$((rdclReplicas * rdclReplicasPerMaster))
    minRepStatefulSet=$((rdclReplicas + minRdclRepSlaves))

    # validate if rediscluster has the minimum replicas
    if [[ $rdclReplicas -lt $minReplicas ]]; then
        resultMessage="ERROR:: Minimum configuration required RedisCluster minReplicas"
    # validate if rediscluster has the minimum replicas per master
    elif [[ $rdclReplicasPerMaster -lt $minReplicasPerMaster ]]; then
        resultMessage="ERROR:: Minimum configuration required RedisCluster minReplicasPerMaster"
    # validate if statefulset creates the minimum replicas per replicas per master (redisCluster.spec.replicas + (redisCluster.spec.replicas*replicasPerMaster))
    elif [[ $minRepStatefulSet -lt $stsReplicas ]]; then
        resultMessage="ERROR:: Minimum configuration required StateFulSet minRepStatefulSet"
    else
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.rediscluster.operator/component=redis' -o custom-columns=':metadata.name')
        for POD in $REDIS_POD; do
            # get and parse the info about nodes configured in the Redis Cluster
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
            # validate if the pod is master
            if [[ "$podType" == *"master"* ]]; then
                ((totalMasters = totalMasters + 1))
            fi
            # validate if the pod is slave
            if [[ "$podType" == *"slave"* ]]; then
                ((totalSlaves = totalSlaves + 1))
            fi
        done
        # validate if exists the minimum pods master configured
        if (($rdclReplicas != $totalMasters)); then
            resultMessage="ERROR:: Minimum configuration required - pods master"
        fi
        # validate if exists the minimum pods slave configured
        if (($minRdclRepSlaves != $totalSlaves)); then
            resultMessage="ERROR:: Minimum configuration required - pods slaves"
        fi
    fi
    echo $resultMessage
}

#######################################
# Allow to validate if  the basic configuration of RedisCluster
# master-slave works properly
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
    local resultMessage="INFO:: RedisCluster configured correctly for Redis master slave configuration"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo "INFO:: Validating basic configuration in Redis Cluster with master-slave configuration"
    message=$(validateRedisMasterSlave)
    if [[ "$message" != "0" ]]; then
        resultMessage=$message
        result=1
    else
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.rediscluster.operator/component=redis' -o custom-columns=':metadata.name')
        for POD in $REDIS_POD; do
            # get and parse the info about nodes configured in the Redis Cluster
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
