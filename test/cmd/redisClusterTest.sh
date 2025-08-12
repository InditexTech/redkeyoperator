#!/bin/bash
#
# Allows to execute differents funtional test for RedisCluster deployments.
# Arguments:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5

set -e

namespace=$1
image=$2
test=$3
newRedisCluster=$4
typeRedisCluster=$5

readonly name="redis-cluster"

#######################################
# Initialize a new RedisCluster validating if exist one installed in the k8 cluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
initializeRedisCluster() {
    REDIS_POD=$(kubectl get po -n $namespace -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    # validate if exists an instance of redisCluster installed
    if [ -z "$REDIS_POD" ]; then
        installRedisCluster
    else
        set +e
        deleteRedisCluster
        cleanAllEnvironment
        set -e
        sleep 10 # wait for terminating status in kubernetes objects
        installRedisCluster
    fi
}

#######################################
# Allows to install RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
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
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-storage.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "ephemeral" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-ephemeral.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedisCluster" == "repmaster" ]]; then
        sed -i "s|TYPE_REDISCLUSTER|redis_v1alpha1_rediscluster-repmaster.yaml|g" "config/samples/kustomization.yaml"
    fi

    make IMG=$image NAMESPACE=$namespace int-test
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
    if [[ "$test" != "All" ]]; then
        if [[ "$result" != "0" ]]; then
            exit $result
        fi
    fi
}

#######################################
# Test for Scale up RedisCluster with patch command.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
scaleUpClusterPatch() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO::patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale Down RedisCluster with patch command.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
scaleDownClusterPatch() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":3}}'
            kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":3}}' --type=merge
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale up RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale Down RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for change the storage in the RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
changeStorage() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$typeRedisCluster" == "storage" ]]; then

        if [[ "$newRedisCluster" == "true" ]]; then
            initializeRedisCluster
        fi
        kubectl get all,rdcl -n $namespace
        echo '##########################################'
        echo 'INFO:: patch cluster with {"spec":{"storage":"1Gi"}}'
        kubectl patch rdcl -n $namespace $name -p '{"spec":{"storage":"1Gi"}}' --type=merge
        echo 'INFO:: waiting for Error status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Error 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rdcl -n $namespace
            echo '##########################################'
            echo 'INFO:: patch cluster with {"spec":{"storage":"500Mi"}}'
            kubectl patch rdcl -n $namespace $name -p '{"spec":{"storage":"500Mi"}}' --type=merge
            echo 'INFO:: waiting for Ready status in redisCluster'
            ./test/cmd/waitforstatus.sh $namespace $name Ready 540
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            fi
        fi
        kubectl get all,rdcl -n $namespace
    else
        resultMessage='INFO:: this test is for RedisCluster with Storage enabled'
        result=0
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for change the storage and replicas in the RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
changeStorageAndReplicas() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$typeRedisCluster" == "storage" ]]; then
        if [[ "$newRedisCluster" == "true" ]]; then
            initializeRedisCluster
        fi
        kubectl get all,rdcl -n $namespace
        echo '##########################################'
        echo 'INFO:: patch cluster with {"spec":{"storage":"1Gi","replicas":6}}'
        kubectl patch rdcl -n $namespace $name -p '{"spec":{"storage":"1Gi","replicas":6}}' --type=merge
        echo 'INFO:: waiting for Error status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Error 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rdcl -n $namespace
            echo '##########################################'
            echo 'INFO:: patch cluster solving the error with {"spec":{"storage":"500Mi"}}'
            kubectl patch rdcl -n $namespace $name -p '{"spec":{"storage":"500Mi"}}' --type=merge
            echo 'INFO:: waiting for ScalingUp status in redisCluster'
            ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            else
                echo 'INFO:: waiting for Ready status in redisCluster'
                ./test/cmd/waitforstatus.sh $namespace $name Ready 540
                if [[ "$result" != "0" ]]; then
                    resultMessage="Failure: Script failed"
                    result=1
                fi
            fi
        fi
        kubectl get all,rdcl -n $namespace
    else
        echo 'INFO:: this test is for RedisCluster with Storage enabled'
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Add a new label in the RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
addLabel() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    echo '############ RedisCluster ############'
    kubectl get rdcl $name -n $namespace -o custom-columns=':spec.labels'
    echo '############ StateFulSet ############'
    kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ Service ############'
    kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ ConfigMap ############'
    kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
    echo '##########################################'
    echo 'INFO:: patch cluster with {"op": "add", "path": "/spec/labels/change", "value": "test"}'
    kubectl patch rdcl -n $namespace $name --type=json -p='[{"op": "add", "path": "/spec/labels/change", "value": "test"}]'
    echo 'INFO:: waiting for Upgrading status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Upgrading 60
    # ./test/cmd/waitforstatus.sh $namespace $name Ready 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Error:: Script failed validating the proper status in RedisCluster [Upgrading]"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Error:: Script failed validating the proper status in RedisCluster [Ready]"
            result=1
        else
            echo '##########################################'
            echo 'INFO:: Validating Labels'
            result=$(validateLabels)
            if [[ "$result" != "0" ]]; then
                resultMessage="Error:: Script failed found differences in labels"
                result=1
            else
                echo '############ RedisCluster ############'
                kubectl get rdcl $name -n $namespace -o custom-columns=':spec.labels'
                echo '############ StateFulSet ############'
                kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
                echo '############ Service ############'
                kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
                echo '############ ConfigMap ############'
                kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Delete a existing label in the RedisCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
deleteLabel() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    echo '############ RedisCluster ############'
    kubectl get rdcl $name -n $namespace -o custom-columns=':spec.labels'
    echo '############ StateFulSet ############'
    kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ Service ############'
    kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ ConfigMap ############'
    kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
    echo '##########################################'
    echo 'INFO:: patch cluster with {"op": "remove", "path": "/spec/labels/team"}'
    kubectl patch rdcl -n $namespace $name --type=json -p='[{"op": "remove", "path": "/spec/labels/team"}]'
    echo 'INFO:: waiting for Upgrading status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Upgrading 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Error:: Script failed validating the proper status in RedisCluster [Upgrading]"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Error:: Script failed validating the proper status in RedisCluster [Ready]"
            result=1
        else
            echo '##########################################'
            echo 'INFO:: Validating Labels'
            result=$(validateLabels)
            if [[ "$result" != "0" ]]; then
                resultMessage="Error:: Script failed found differences in labels"
                result=1
            else
                echo '############ RedisCluster ############'
                kubectl get rdcl $name -n $namespace -o custom-columns=':spec.labels'
                echo '############ StateFulSet ############'
                kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
                echo '############ Service ############'
                kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
                echo '############ ConfigMap ############'
                kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allows to validate if labels were changed properly
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
validateLabels() {
    local defaultRedisClusterNameLabel="redkey-cluster-name"
    local defaultRedisClusterOperatorLabel="redis.redkeycluster.operator/component"
    # get RedisCluster labels
    local rdclLabels=$(getLabels "kubectl get rdcl redis-cluster -o custom-columns=':spec.labels' -n $namespace")
    # get ConfigMap labels
    local cmLabels=$(getLabels "kubectl get cm redis-cluster -o custom-columns=':metadata.labels' -n $namespace")
    # get Service labels
    local svcLabels=$(getLabels "kubectl get svc redis-cluster -o custom-columns=':metadata.labels' -n $namespace")
    # get StateFulSet labels
    local stsLabels=$(getLabels "kubectl get sts redis-cluster -o custom-columns=':metadata.labels' -n $namespace")

    local existLabel=0
    local result=0

    # validate when was added a new label
    for rdclLabel in $rdclLabels; do
        existLabel=0
        # validate differences with configmap
        for cmLabel in $cmLabels; do
            if [[ "$rdclLabel" == "$cmLabel" ]]; then
                existLabel=1
                break
            fi
        done
        if [[ "$existLabel" != "1" ]]; then
            # Failure: Script failed, Error:: ConfigMap labels are diferents
            result=1
            break
        fi
        # validate differences with service
        for svsLabel in $svcLabels; do
            if [[ "$rdclLabel" == "$svsLabel" ]]; then
                existLabel=1
                break
            fi
        done
        if [[ "$existLabel" != "1" ]]; then
            # Failure: Script failed, Error:: Service labels are diferents
            result=1
            break
        fi
        # validate differences with StateFulSet
        for stsLabel in $stsLabels; do
            if [[ "$rdclLabel" == "$stsLabel" ]]; then
                existLabel=1
                break
            fi
        done
        if [[ "$existLabel" != "1" ]]; then
            #  Failure: Script failed, Error:: statefulSet labels are diferents
            result=1
            break
        fi
    done

    # validate when was deleted a label
    # validate statefulset labels
    if [[ "$result" == "0" ]]; then
        for stsLabel in $stsLabels; do
            existLabel=0
            set -f # avoid globbing (expansion of *).
            array=(${stsLabel//:/ })
            # don't check the default labels
            if [[ "${array[0]}" == "$defaultRedisClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedisClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rdclLabel in $rdclLabels; do
                    if [[ "$stsLabel" == "$rdclLabel" ]]; then
                        existLabel=1
                        break
                    fi
                done
            fi
            if [[ "$existLabel" != "1" ]]; then
                #  Failure: Script failed, Error:: statefulSet labels are diferents
                result=1
                break
            fi
        done
    fi

    # validate service labels
    if [[ "$result" == "0" ]]; then
        for svsLabel in $svcLabels; do
            existLabel=0
            set -f # avoid globbing (expansion of *).
            array=(${svsLabel//:/ })
            # don't check the default labels
            if [[ "${array[0]}" == "$defaultRedisClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedisClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rdclLabel in $rdclLabels; do
                    if [[ "$svsLabel" == "$rdclLabel" ]]; then
                        existLabel=1
                        break
                    fi
                done
            fi
            if [[ "$existLabel" != "1" ]]; then
                #  Failure: Script failed, Error:: statefulSet labels are diferents
                result=1
                break
            fi
        done
    fi

    # validate configmap labels
    if [[ "$result" == "0" ]]; then
        for cmLabel in $cmLabels; do
            existLabel=0
            set -f # avoid globbing (expansion of *).
            array=(${cmLabel//:/ })
            # don't check the default labels
            if [[ "${array[0]}" == "$defaultRedisClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedisClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rdclLabel in $rdclLabels; do
                    if [[ "$cmLabel" == "$rdclLabel" ]]; then
                        existLabel=1
                        break
                    fi
                done
            fi
            if [[ "$existLabel" != "1" ]]; then
                #  Failure: Script failed, Error:: statefulSet labels are diferents
                result=1
                break
            fi
        done
    fi

    echo $result
}

#######################################
# # Allows to get the labels associated to a specific object in k8
# Globals:
#   none
# Arguments:
#   query
#######################################
getLabels() {
    local query=$1
    local labelsList
    labels="$(eval $query | sed 's/map\[//g' | sed 's/\]//g')"
    labelsList=$(echo $labels | tr " " "\n")
    echo $labelsList
}

#######################################
# Remove all object in a cluster
# Globals:
#   None
# Arguments:
#   None
#######################################
cleanAllEnvironment() {
    make int-test-clean
}

#######################################
# Remove a existing RedisCluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
deleteRedisCluster() {
    local result=0
    local resultMessage="INFO:: RedisCluster was deleted correctly"
    echo '##########################################'
    echo 'INFO:: getting data and objects to delete in the current cluster'
    pvcsToDelete=$(kubectl get pvc -l='redkey-cluster-name=redis-cluster' -o custom-columns=':metadata.name' -n $namespace)
    rdclPersistent=$(kubectl get rdcl -l='app=redis' -o custom-columns=':metadata.name' -n $namespace)
    rdclEphemeral=$(kubectl get rdcl -l='tier=redis-cluster' -o custom-columns=':metadata.name' -n $namespace)

    if [ -z "$rdclPersistent" ] && [ -z "$rdclEphemeral" ]; then
        echo "INFO:: No rdcl to delete."
    else
        kubectl delete rdcl redis-cluster
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        fi
    fi

    if [ -z "$pvcsToDelete" ]; then
        echo "INFO:: No pvc to delete."
    else
        for PVC in $pvcsToDelete; do
            echo $PVC
            kubectl delete pvc/$PVC
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
                break
            fi
        done
    fi
    echo $resultMessage
}

#######################################
# Allows to validate if is posible to insert data into the Redis Cluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
insertData() {
    # make redis-insert
    local result=0
    local resultMessage="INFO:: RedisCluster insert data correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    if [ -z "$REDIS_POD" ]; then
        result=0
        resultMessage="ERROR:: No pods running to insert data, starts your cluster to use this feature."
    else
        rm -f /tmp/data.txt
        for ((i = 1; i <= 100; i++)); do echo "set ${RANDOM}${RANDOM} ${RANDOM}${RANDOM}" >>/tmp/data.txt; done
        cat /tmp/data.txt | kubectl -n $namespace exec -i $REDIS_POD -- redis-cli -c
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allows to validate if is posible to insert data while scaling up event is executing into the Redis Cluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
insertDataWhileScaling() {
    local result=0
    local inserts=0
    local keysByLoop=10
    local totalKeysInPods=0
    local totalKeys=0
    local resultMessage="INFO:: RedisCluster insert data correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo 'INFO::patch cluster with {"spec":{"replicas":3}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":3}}' --type=merge
    sleep 3s
    for ((i = 1; i <= 560; i++)); do
        ((inserts = inserts + 1))
        echo '##########################################'
        echo $inserts
        status=$(kubectl get rediscluster -n $namespace $name --template='{{.status.status}}')
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
        if [ -z "$REDIS_POD" ]; then
            result=1
            resultMessage="ERROR:: No pods running to insert data."
            break
        else
            echo 'INFO::Insert data while execute the scaling event'
            rm -f /tmp/data.txt
            for ((i = 1; i <= $keysByLoop; i++)); do
                echo "set ${RANDOM}${RANDOM} ${RANDOM}${RANDOM}" >>/tmp/data.txt
            done
            cat /tmp/data.txt | kubectl -n $namespace exec -i $REDIS_POD -- redis-cli -c
        fi
        if [[ "$status" == "Ready" ]]; then
            break
        fi
        sleep 2s
    done
    if [[ "$result" == "0" ]]; then
        kubectl get all,rdcl -n $namespace
        echo '##########################################'
        totalKeys=$((inserts * keysByLoop))
        echo "INFO:: total keys inserted while the scale up process was executed $totalKeys"
        # REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
        if [ -z "$REDIS_POD" ]; then
            result=1
            resultMessage="ERROR:: No pods running to get data."
        else
            for POD in $REDIS_POD; do
                count=$(kubectl -n $namespace exec -i $POD -- redis-cli DBSIZE)
                countKeys=$(echo $count | sed 's/ *$//g')    # clear white space in the string
                countKeys=$(echo "$countKeys" | tr -d $'\r') # clear special characteres in the string
                echo "INFO:: redis dbsize pod: $POD count-keys: $countKeys"
                totalKeysInPods=$(expr $countKeys + $totalKeysInPods)
                echo "INFO:: summary redis dbsize $totalKeysInPods"
            done
            echo '##########################################'
            echo "INFO:: total keys inserted in RedisCluster: $totalKeys"
            echo "INFO:: total keys in pods RedisCluster: $totalKeysInPods"
            if [[ "$totalKeys" != "$totalKeysInPods" ]]; then
                resultMessage="ERROR:: the insert keys process don't work properly REDIS-CHECK"
                result=1
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allows to validate if is posible to insert data while scaling down event is executing into the Redis Cluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
insertDataWhileScalingDown() {
    local result=0
    local keysByLoop=10
    local inserts=0
    local totalKeysInPods=0
    local totalKeys=0
    local resultMessage="INFO:: RedisCluster insert data correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":2}}'
            kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":2}}' --type=merge
            sleep 3s
            for ((i = 1; i <= 560; i++)); do
                ((inserts = inserts + 1))
                echo '##########################################'
                echo $inserts
                status=$(kubectl get rediscluster -n $namespace $name --template='{{.status.status}}')
                REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
                if [ -z "$REDIS_POD" ]; then
                    result=1
                    resultMessage="ERROR:: No pods running to insert data."
                    break
                else
                    echo 'INFO::Insert data while execute the scaling event'
                    rm -f /tmp/data.txt
                    for ((i = 1; i <= $keysByLoop; i++)); do
                        echo "set ${RANDOM}${RANDOM} ${RANDOM}${RANDOM}" >>/tmp/data.txt
                    done
                    cat /tmp/data.txt | kubectl -n $namespace exec -i $REDIS_POD -- redis-cli -c
                fi
                if [[ "$status" == "Ready" ]]; then
                    break
                fi
                sleep 2s
            done
        fi
    fi

    if [[ "$result" == "0" ]]; then
        kubectl get all,rdcl -n $namespace
        echo '##########################################'
        totalKeys=$((inserts * keysByLoop))
        echo "INFO:: total keys inserted while the scale up process was executed $totalKeys"
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
        if [ -z "$REDIS_POD" ]; then
            result=1
            resultMessage="ERROR:: No pods running to get data."
        else
            for POD in $REDIS_POD; do
                count=$(kubectl -n $namespace exec -i $POD -- redis-cli DBSIZE)
                countKeys=$(echo $count | sed 's/ *$//g')    # clear white space in the string
                countKeys=$(echo "$countKeys" | tr -d $'\r') # clear special characteres in the string
                echo "INFO:: redis dbsize pod: $POD count-keys: $countKeys"
                totalKeysInPods=$(expr $countKeys + $totalKeysInPods)
                echo "INFO:: summary redis dbsize $totalKeysInPods"
            done
            echo '##########################################'
            echo "INFO:: total keys inserted in RedisCluster: $totalKeys"
            echo "INFO:: total keys in pods RedisCluster: $totalKeysInPods"
            if [[ "$totalKeys" != "$totalKeysInPods" ]]; then
                resultMessage="ERROR:: the insert keys process don't work properly REDIS-CHECK"
                result=1
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allows to get a specific key inserted into the Redis Cluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
getSpecificKey() {
    local result=0
    local key="foo"
    local value="bar"
    local getValue
    local resultMessage="INFO:: Get Specific key test run correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    if [ -z "$REDIS_POD" ]; then
        result=1
        resultMessage="ERROR:: No pods running to insert data, starts your cluster to use this feature."
    else
        # Adding a specific key
        echo "INFO:: set $key $value"
        kubectl -n $namespace exec -i $REDIS_POD -- redis-cli set $key $value

        # Adding randoms keys
        rm -f /tmp/data.txt
        for ((i = 1; i <= 100; i++)); do echo "set ${RANDOM}${RANDOM} ${RANDOM}${RANDOM}" >>/tmp/data.txt; done
        cat /tmp/data.txt
        echo "INFO kubectl -n $namespace exec -i $REDIS_POD -- redis-cli -c"
        cat /tmp/data.txt | kubectl -n $namespace exec -i $REDIS_POD -- redis-cli -c

        # loop through the redis Cluster pods looking for the key.
        for POD in $REDIS_POD; do
            getValue=$(kubectl -n $namespace exec -i $POD -- redis-cli get $key)
            if [ -n "$getValue" ]; then
                break
            fi
        done

        if [ -z "$getValue" ]; then
            result=1
            resultMessage="ERROR:: no key value data found."
        elif [[ "$value" == "$getValue" ]]; then
            resultMessage="INFO:: Get Specific key test run correctly key: $key - value: $getValue"
        fi
    fi
    echo '##########################################'
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Convert the label info to Array info
# Globals:
#   None
# Arguments:
#   None
#######################################
getLabels() {
    local query=$1
    local labelsList
    labels="$(eval $query | sed 's/map\[//g' | sed 's/\]//g')"
    labelsList=$(echo $labels | tr " " "\n")
    echo $labelsList
}

#######################################
# Allow to validate if RedisCluster master-slave works properly
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
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
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
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
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
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
        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
        for POD in $REDIS_POD; do
            # get and parse the info about nodes configured in the Redis Cluster
            kubectl -n $namespace exec -i $POD -- redis-cli CLUSTER NODES
            echo '##########################################'
            break
        done
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allow to validate if the configuration of RedisCluster master-slave works properly,
# while this one is Scaling Up
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
scalingUpRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":4,"replicasPerMaster":2}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":4,"replicasPerMaster":2}}' --type=merge
    echo 'INFO::waiting for ScalingUp status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR:: Failure process the scaling up of the RedisCluster"
        result=1
    else
        echo 'INFO::waiting for Ready status in redisCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure process the wait for RedisCluster Ready"
            result=1
        else
            kubectl get all,rdcl -n $namespace
            echo '##########################################'
            echo "INFO:: Validating configuration in Redis Cluster with master-slave configuration"
            message=$(validateRedisMasterSlave)
            if [[ "$message" != "0" ]]; then
                resultMessage=$message
                result=1
            else
                # show the configuration
                REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
                for POD in $REDIS_POD; do
                    # get and parse the info about nodes configured in the Redis Cluster
                    kubectl -n $namespace exec -i $POD -- redis-cli CLUSTER NODES
                    echo '##########################################'
                    break
                done
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allow to validate if the configuration of RedisCluster master-slave works properly,
# while this one is Scaling Down
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
scalingDownRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":4,"replicasPerMaster":2}}'
    kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":4,"replicasPerMaster":2}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":3},"replicasPerMaster":1}'
            kubectl patch rdcl -n $namespace $name -p '{"spec":{"replicas":3,"replicasPerMaster":1}}' --type=merge
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
                else
                    kubectl get all,rdcl -n $namespace
                    echo '##########################################'
                    echo "INFO:: Validating configuration in Redis Cluster with master-slave configuration"
                    sleep 50
                    message=$(validateRedisMasterSlave)
                    if [[ "$message" != "0" ]]; then
                        resultMessage=$message
                        result=1
                    else
                        # show the configuration
                        REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
                        for POD in $REDIS_POD; do
                            # get and parse the info about nodes configured in the Redis Cluster
                            kubectl -n $namespace exec -i $POD -- redis-cli CLUSTER NODES
                            echo '##########################################'
                            break
                        done
                    fi
                fi
            fi
        fi
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allow to validate if RedisCluster master-slave works properly,
# while a pod is not working well
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
killPodRedisMasterSlave() {

    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedisCluster" == "true" ]]; then
        initializeRedisCluster
    fi
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
    for POD in $REDIS_POD; do
        set +e
        # kill one pod that is allocated to RedisCluster
        echo 'INFO:: Killing one pod'
        error=$(kubectl -n $namespace exec -i $POD -- redis-cli debug segfault 30)
        set -e
        break
    done
    kubectl get all,rdcl -n $namespace
    echo '##########################################'
    echo 'INFO:: waiting for Ready status in redisCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Ready 540
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Failure: Script failed"
        result=1
    else
        echo "INFO:: Validating configuration in Redis Cluster with master-slave configuration"
        sleep 40
        kubectl get all,rdcl -n $namespace
        echo '##########################################'
        message=$(validateRedisMasterSlave)
        if [[ "$message" != "0" ]]; then
            resultMessage=$message
            result=1
        fi
    fi

    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Allow to execute all funtional test
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
allTest() {
    scaleUpCluster
    scaleDownCluster
    changeStorage
    changeStorageAndReplicas
    addLabel
    deleteLabel
    InsertData
    InsertDataWhileScaling
}

#######################################
# Initialize a new redis operator in local environment.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedisCluster=$4
#   typeRedisCluster=$5
# Arguments:
#   None
#######################################
initializeOperator() {
    make docker-build docker-push IMG="$image"
    set +e
    deleteRedisCluster
    cleanAllEnvironment
    set -e
    sleep 10 # wait for terminating status in kubernetes objects
    make IMG=$image NAMESPACE=$namespace int-test
    deleteRedisCluster
}

#######################################
# Delete all environment.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
# Arguments:
#   None
#######################################
deleteAll() {
    echo '################# Delete objects #########################'
    set +e
    deleteRedisCluster
    echo '##########################################'
    cleanAllEnvironment
    set -e
    sleep 10
    echo '################# end delete objects #########################'
}

echo "##### NAMESPACE=$namespace, NAME=$name, IMAGE=$image TEST=$test NEW_REDISCLUSTER=$newRedisCluster TYPE_REDISCLUSTER=$typeRedisCluster #####"

case "${test}" in
ScalingUpPatch)
    scaleUpClusterPatch
    ;;
ScalingDownPatch)
    scaleDownClusterPatch
    ;;
ScalingUp)
    scaleUpCluster
    ;;
ScalingDown)
    scaleDownCluster
    ;;
ChangeStorage)
    changeStorage
    ;;
ChangeStorageReplicas)
    changeStorageAndReplicas
    ;;
AddLabel)
    addLabel
    ;;
DeleteLabel)
    deleteLabel
    ;;
InsertData)
    insertData
    ;;
InsertDataWhileScaling)
    insertDataWhileScaling
    ;;
InsertDataWhileScalingDown)
    insertDataWhileScalingDown
    ;;
GetSpecificKey)
    getSpecificKey
    ;;
ValidateBasicRedisMasterSlave)
    validateBasicRedisMasterSlave
    ;;
ScalingUpRedisMasterSlave)
    scalingUpRedisMasterSlave
    ;;
ScalingDownRedisMasterSlave)
    scalingDownRedisMasterSlave
    ;;
KillPodRedisMasterSlave)
    killPodRedisMasterSlave
    ;;
DeleteRedisCluster)
    deleteRedisCluster
    ;;
DeleteAll)
    deleteAll
    ;;
InitializeOperator)
    initializeOperator
    ;;
All)
    allTest
    ;;
*)
    echo "please select a correct test"
    ;;
esac

# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "Initialize" "true" "storage"
# ./test/cmd/redisClusterTest.sh test-${{ github.event.pull_request.head.sha }} ${{env.JFROG_SNAPSHOT_REGISTRY}}/redis-operator:sha-${{ github.event.pull_request.head.sha }} "Initialize" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:1.0.2" "ScalingUpPatch" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDownPatch" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:1.0.2" "ScalingUp" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDown" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeStorage" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeStorageReplicas" "false" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "AddLabel" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "AddLabel" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteLabel" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteLabel" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeLabels" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeLabels" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertData" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertData" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertDataWhileScaling" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertDataWhileScalingDown" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "GetSpecificKey" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ValidateBasicRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingUpRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDownRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "KillPodRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "MasterSlave" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "All" "true" "storage"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteRedisCluster"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InitializeOperator" "false" "false"
# ./test/cmd/redisClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteAll" "false" "false"
