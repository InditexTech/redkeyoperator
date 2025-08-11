#!/bin/bash
#
# Allows to execute differents funtional test for RedKeyCluster deployments.
# Arguments:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5

set -e

namespace=$1
image=$2
test=$3
newRedKeyCluster=$4
typeRedKeyCluster=$5

readonly name="redkey-cluster"

#######################################
# Initialize a new RedKeyCluster validating if exist one installed in the k8 cluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
initializeRedKeyCluster() {
    REDIS_POD=$(kubectl get po -n $namespace -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head)
    # validate if exists an instance of redkeyCluster installed
    if [ -z "$REDIS_POD" ]; then
        installRedKeyCluster
    else
        set +e
        deleteRedKeyCluster
        cleanAllEnvironment
        set -e
        sleep 10 # wait for terminating status in kubernetes objects
        installRedKeyCluster
    fi
}

#######################################
# Allows to install RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
installRedKeyCluster() {
    local result=0
    local resultMessage="INFO:: RedKeyCluster created and run correctly"
    rm -f config/samples/kustomization.yaml
    cat <<EOF >config/samples/kustomization.yaml
resources:
 - TYPE_REDKEYCLUSTER
EOF
    if [[ "$typeRedKeyCluster" == "storage" ]]; then
        sed -i "s|TYPE_REDKEYCLUSTER|redis_v1alpha1_redkeycluster-storage.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedKeyCluster" == "ephemeral" ]]; then
        sed -i "s|TYPE_REDKEYCLUSTER|redis_v1alpha1_redkeycluster-ephemeral.yaml|g" "config/samples/kustomization.yaml"
    elif [[ "$typeRedKeyCluster" == "repmaster" ]]; then
        sed -i "s|TYPE_REDKEYCLUSTER|redis_v1alpha1_redkeycluster-repmaster.yaml|g" "config/samples/kustomization.yaml"
    fi

    make IMG=$image NAMESPACE=$namespace int-test
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
    if [[ "$test" != "All" ]]; then
        if [[ "$result" != "0" ]]; then
            exit $result
        fi
    fi
}

#######################################
# Test for Scale up RedKeyCluster with patch command.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scaleUpClusterPatch() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO::patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale Down RedKeyCluster with patch command.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scaleDownClusterPatch() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":3}}'
            kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":3}}' --type=merge
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale up RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scaleUpCluster() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Scale Down RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scaleDownCluster() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
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
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for change the storage in the RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
changeStorage() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$typeRedKeyCluster" == "storage" ]]; then

        if [[ "$newRedKeyCluster" == "true" ]]; then
            initializeRedKeyCluster
        fi
        kubectl get all,rkcl -n $namespace
        echo '##########################################'
        echo 'INFO:: patch cluster with {"spec":{"storage":"1Gi"}}'
        kubectl patch rkcl -n $namespace $name -p '{"spec":{"storage":"1Gi"}}' --type=merge
        echo 'INFO:: waiting for Error status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Error 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rkcl -n $namespace
            echo '##########################################'
            echo 'INFO:: patch cluster with {"spec":{"storage":"500Mi"}}'
            kubectl patch rkcl -n $namespace $name -p '{"spec":{"storage":"500Mi"}}' --type=merge
            echo 'INFO:: waiting for Ready status in redkeyCluster'
            ./test/cmd/waitforstatus.sh $namespace $name Ready 540
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            fi
        fi
        kubectl get all,rkcl -n $namespace
    else
        resultMessage='INFO:: this test is for RedKeyCluster with Storage enabled'
        result=0
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for change the storage and replicas in the RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
changeStorageAndReplicas() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$typeRedKeyCluster" == "storage" ]]; then
        if [[ "$newRedKeyCluster" == "true" ]]; then
            initializeRedKeyCluster
        fi
        kubectl get all,rkcl -n $namespace
        echo '##########################################'
        echo 'INFO:: patch cluster with {"spec":{"storage":"1Gi","replicas":6}}'
        kubectl patch rkcl -n $namespace $name -p '{"spec":{"storage":"1Gi","replicas":6}}' --type=merge
        echo 'INFO:: waiting for Error status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Error 60
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Failure: Script failed"
            result=1
        else
            kubectl get all,rkcl -n $namespace
            echo '##########################################'
            echo 'INFO:: patch cluster solving the error with {"spec":{"storage":"500Mi"}}'
            kubectl patch rkcl -n $namespace $name -p '{"spec":{"storage":"500Mi"}}' --type=merge
            echo 'INFO:: waiting for ScalingUp status in redkeyCluster'
            ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
            result=$?
            if [[ "$result" != "0" ]]; then
                resultMessage="Failure: Script failed"
                result=1
            else
                echo 'INFO:: waiting for Ready status in redkeyCluster'
                ./test/cmd/waitforstatus.sh $namespace $name Ready 540
                if [[ "$result" != "0" ]]; then
                    resultMessage="Failure: Script failed"
                    result=1
                fi
            fi
        fi
        kubectl get all,rkcl -n $namespace
    else
        echo 'INFO:: this test is for RedKeyCluster with Storage enabled'
    fi
    echo $resultMessage
    if [[ "$test" != "All" ]]; then
        exit $result
    fi
}

#######################################
# Test for Add a new label in the RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
addLabel() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    echo '############ RedKeyCluster ############'
    kubectl get rkcl $name -n $namespace -o custom-columns=':spec.labels'
    echo '############ StateFulSet ############'
    kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ Service ############'
    kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ ConfigMap ############'
    kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
    echo '##########################################'
    echo 'INFO:: patch cluster with {"op": "add", "path": "/spec/labels/change", "value": "test"}'
    kubectl patch rkcl -n $namespace $name --type=json -p='[{"op": "add", "path": "/spec/labels/change", "value": "test"}]'
    echo 'INFO:: waiting for Upgrading status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Upgrading 60
    # ./test/cmd/waitforstatus.sh $namespace $name Ready 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Error:: Script failed validating the proper status in RedKeyCluster [Upgrading]"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Error:: Script failed validating the proper status in RedKeyCluster [Ready]"
            result=1
        else
            echo '##########################################'
            echo 'INFO:: Validating Labels'
            result=$(validateLabels)
            if [[ "$result" != "0" ]]; then
                resultMessage="Error:: Script failed found differences in labels"
                result=1
            else
                echo '############ RedKeyCluster ############'
                kubectl get rkcl $name -n $namespace -o custom-columns=':spec.labels'
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
# Test for Delete a existing label in the RedKeyCluster.
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
deleteLabel() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    echo '############ RedKeyCluster ############'
    kubectl get rkcl $name -n $namespace -o custom-columns=':spec.labels'
    echo '############ StateFulSet ############'
    kubectl get sts $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ Service ############'
    kubectl get svc $name -n $namespace -o custom-columns=':metadata.labels'
    echo '############ ConfigMap ############'
    kubectl get cm $name -n $namespace -o custom-columns=':metadata.labels'
    echo '##########################################'
    echo 'INFO:: patch cluster with {"op": "remove", "path": "/spec/labels/team"}'
    kubectl patch rkcl -n $namespace $name --type=json -p='[{"op": "remove", "path": "/spec/labels/team"}]'
    echo 'INFO:: waiting for Upgrading status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Upgrading 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Error:: Script failed validating the proper status in RedKeyCluster [Upgrading]"
        result=1
    else
        echo '##########################################'
        echo 'INFO:: waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="Error:: Script failed validating the proper status in RedKeyCluster [Ready]"
            result=1
        else
            echo '##########################################'
            echo 'INFO:: Validating Labels'
            result=$(validateLabels)
            if [[ "$result" != "0" ]]; then
                resultMessage="Error:: Script failed found differences in labels"
                result=1
            else
                echo '############ RedKeyCluster ############'
                kubectl get rkcl $name -n $namespace -o custom-columns=':spec.labels'
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
validateLabels() {
    local defaultRedKeyClusterNameLabel="redkey-cluster-name"
    local defaultRedKeyClusterOperatorLabel="redis.redkeycluster.operator/component"
    # get RedKeyCluster labels
    local rkclLabels=$(getLabels "kubectl get rkcl redkey-cluster -o custom-columns=':spec.labels' -n $namespace")
    # get ConfigMap labels
    local cmLabels=$(getLabels "kubectl get cm redkey-cluster -o custom-columns=':metadata.labels' -n $namespace")
    # get Service labels
    local svcLabels=$(getLabels "kubectl get svc redkey-cluster -o custom-columns=':metadata.labels' -n $namespace")
    # get StateFulSet labels
    local stsLabels=$(getLabels "kubectl get sts redkey-cluster -o custom-columns=':metadata.labels' -n $namespace")

    local existLabel=0
    local result=0

    # validate when was added a new label
    for rkclLabel in $rkclLabels; do
        existLabel=0
        # validate differences with configmap
        for cmLabel in $cmLabels; do
            if [[ "$rkclLabel" == "$cmLabel" ]]; then
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
            if [[ "$rkclLabel" == "$svsLabel" ]]; then
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
            if [[ "$rkclLabel" == "$stsLabel" ]]; then
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
            if [[ "${array[0]}" == "$defaultRedKeyClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedKeyClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rkclLabel in $rkclLabels; do
                    if [[ "$stsLabel" == "$rkclLabel" ]]; then
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
            if [[ "${array[0]}" == "$defaultRedKeyClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedKeyClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rkclLabel in $rkclLabels; do
                    if [[ "$svsLabel" == "$rkclLabel" ]]; then
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
            if [[ "${array[0]}" == "$defaultRedKeyClusterNameLabel" ]]; then
                existLabel=1
            elif [[ "${array[0]}" == "$defaultRedKeyClusterOperatorLabel" ]]; then
                existLabel=1
            else
                for rkclLabel in $rkclLabels; do
                    if [[ "$cmLabel" == "$rkclLabel" ]]; then
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
# Remove a existing RedKeyCluster
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
deleteRedKeyCluster() {
    local result=0
    local resultMessage="INFO:: RedKeyCluster was deleted correctly"
    echo '##########################################'
    echo 'INFO:: getting data and objects to delete in the current cluster'
    pvcsToDelete=$(kubectl get pvc -l='redkey-cluster-name=redkey-cluster' -o custom-columns=':metadata.name' -n $namespace)
    rkclPersistent=$(kubectl get rkcl -l='app=redis' -o custom-columns=':metadata.name' -n $namespace)
    rkclEphemeral=$(kubectl get rkcl -l='tier=redkey-cluster' -o custom-columns=':metadata.name' -n $namespace)

    if [ -z "$rkclPersistent" ] && [ -z "$rkclEphemeral" ]; then
        echo "INFO:: No rkcl to delete."
    else
        kubectl delete rkcl redkey-cluster
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
insertData() {
    # make redis-insert
    local result=0
    local resultMessage="INFO:: RedKeyCluster insert data correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
insertDataWhileScaling() {
    local result=0
    local inserts=0
    local keysByLoop=10
    local totalKeysInPods=0
    local totalKeys=0
    local resultMessage="INFO:: RedKeyCluster insert data correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo 'INFO::patch cluster with {"spec":{"replicas":3}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":3}}' --type=merge
    sleep 3s
    for ((i = 1; i <= 560; i++)); do
        ((inserts = inserts + 1))
        echo '##########################################'
        echo $inserts
        status=$(kubectl get redkeycluster -n $namespace $name --template='{{.status.status}}')
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
        kubectl get all,rkcl -n $namespace
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
            echo "INFO:: total keys inserted in RedKeyCluster: $totalKeys"
            echo "INFO:: total keys in pods RedKeyCluster: $totalKeysInPods"
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
insertDataWhileScalingDown() {
    local result=0
    local keysByLoop=10
    local inserts=0
    local totalKeysInPods=0
    local totalKeys=0
    local resultMessage="INFO:: RedKeyCluster insert data correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":6}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":6}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":2}}'
            kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":2}}' --type=merge
            sleep 3s
            for ((i = 1; i <= 560; i++)); do
                ((inserts = inserts + 1))
                echo '##########################################'
                echo $inserts
                status=$(kubectl get redkeycluster -n $namespace $name --template='{{.status.status}}')
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
        kubectl get all,rkcl -n $namespace
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
            echo "INFO:: total keys inserted in RedKeyCluster: $totalKeys"
            echo "INFO:: total keys in pods RedKeyCluster: $totalKeysInPods"
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
getSpecificKey() {
    local result=0
    local key="foo"
    local value="bar"
    local getValue
    local resultMessage="INFO:: Get Specific key test run correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
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
# Allow to validate if RedKeyCluster master-slave works properly
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
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
    # get replicas for RedKeyCluster
    local rkclReplicas=$(kubectl -n $namespace get rkcl $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas per master for RedKeyCluster
    local rkclReplicasPerMaster=$(kubectl -n $namespace get rkcl $name -o custom-columns=':spec.replicasPerMaster' | sed 's/ *$//g' | tr -d $'\r')
    # get replicas for statefulset associate to RedKeyCluster
    local stsReplicas=$(kubectl -n $namespace get sts $name -o custom-columns=':spec.replicas' | sed 's/ *$//g' | tr -d $'\r')
    # get minimum replicas calculate for StateFulSet
    minRkclRepSlaves=$((rkclReplicas * rkclReplicasPerMaster))
    minRepStatefulSet=$((rkclReplicas + minRkclRepSlaves))

    # validate if redkeycluster has the minimum replicas
    if [[ $rkclReplicas -lt $minReplicas ]]; then
        resultMessage="ERROR:: Minimum configuration required RedKeyCluster minReplicas"
    # validate if redkeycluster has the minimum replicas per master
    elif [[ $rkclReplicasPerMaster -lt $minReplicasPerMaster ]]; then
        resultMessage="ERROR:: Minimum configuration required RedKeyCluster minReplicasPerMaster"
    # validate if statefulset creates the minimum replicas per replicas per master (redkeyCluster.spec.replicas + (redkeyCluster.spec.replicas*replicasPerMaster))
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
        if (($rkclReplicas != $totalMasters)); then
            resultMessage="ERROR:: Minimum configuration required - pods master"
        fi
        # validate if exists the minimum pods slave configured
        if (($minRkclRepSlaves != $totalSlaves)); then
            resultMessage="ERROR:: Minimum configuration required - pods slaves"
        fi
    fi
    echo $resultMessage
}

#######################################
# Allow to validate if  the basic configuration of RedKeyCluster
# master-slave works properly
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
validateBasicRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: RedKeyCluster configured correctly for Redis master slave configuration"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
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
# Allow to validate if the configuration of RedKeyCluster master-slave works properly,
# while this one is Scaling Up
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scalingUpRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":4,"replicasPerMaster":2}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":4,"replicasPerMaster":2}}' --type=merge
    echo 'INFO::waiting for ScalingUp status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name ScalingUp 60
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="ERROR:: Failure process the scaling up of the RedKeyCluster"
        result=1
    else
        echo 'INFO::waiting for Ready status in redkeyCluster'
        ./test/cmd/waitforstatus.sh $namespace $name Ready 540
        result=$?
        if [[ "$result" != "0" ]]; then
            resultMessage="ERROR::Failure process the wait for RedKeyCluster Ready"
            result=1
        else
            kubectl get all,rkcl -n $namespace
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
# Allow to validate if the configuration of RedKeyCluster master-slave works properly,
# while this one is Scaling Down
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
scalingDownRedisMasterSlave() {
    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: patch cluster with {"spec":{"replicas":4,"replicasPerMaster":2}}'
    kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":4,"replicasPerMaster":2}}' --type=merge
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
            echo 'INFO:: patch cluster with {"spec":{"replicas":3},"replicasPerMaster":1}'
            kubectl patch rkcl -n $namespace $name -p '{"spec":{"replicas":3,"replicasPerMaster":1}}' --type=merge
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
                else
                    kubectl get all,rkcl -n $namespace
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
# Allow to validate if RedKeyCluster master-slave works properly,
# while a pod is not working well
# Globals:
#   namespace=$1
#   image=$2
#   test=$3
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
killPodRedisMasterSlave() {

    local result=0
    local resultMessage="INFO:: Test runs correctly"
    if [[ "$newRedKeyCluster" == "true" ]]; then
        initializeRedKeyCluster
    fi
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    REDIS_POD=$(kubectl get po -n $namespace --field-selector=status.phase=Running -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name')
    for POD in $REDIS_POD; do
        set +e
        # kill one pod that is allocated to RedKeyCluster
        echo 'INFO:: Killing one pod'
        error=$(kubectl -n $namespace exec -i $POD -- redis-cli debug segfault 30)
        set -e
        break
    done
    kubectl get all,rkcl -n $namespace
    echo '##########################################'
    echo 'INFO:: waiting for Ready status in redkeyCluster'
    ./test/cmd/waitforstatus.sh $namespace $name Ready 540
    result=$?
    if [[ "$result" != "0" ]]; then
        resultMessage="Failure: Script failed"
        result=1
    else
        echo "INFO:: Validating configuration in Redis Cluster with master-slave configuration"
        sleep 40
        kubectl get all,rkcl -n $namespace
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
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
#   newRedKeyCluster=$4
#   typeRedKeyCluster=$5
# Arguments:
#   None
#######################################
initializeOperator() {
    make docker-build docker-push IMG="$image"
    set +e
    deleteRedKeyCluster
    cleanAllEnvironment
    set -e
    sleep 10 # wait for terminating status in kubernetes objects
    make IMG=$image NAMESPACE=$namespace int-test
    deleteRedKeyCluster
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
    deleteRedKeyCluster
    echo '##########################################'
    cleanAllEnvironment
    set -e
    sleep 10
    echo '################# end delete objects #########################'
}

echo "##### NAMESPACE=$namespace, NAME=$name, IMAGE=$image TEST=$test NEW_REDKEYCLUSTER=$newRedKeyCluster TYPE_REDKEYCLUSTER=$typeRedKeyCluster #####"

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
DeleteRedKeyCluster)
    deleteRedKeyCluster
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

# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "Initialize" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh test-${{ github.event.pull_request.head.sha }} ${{env.JFROG_SNAPSHOT_REGISTRY}}/redis-operator:sha-${{ github.event.pull_request.head.sha }} "Initialize" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:1.0.2" "ScalingUpPatch" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDownPatch" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:1.0.2" "ScalingUp" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDown" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeStorage" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeStorageReplicas" "false" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "AddLabel" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "AddLabel" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteLabel" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteLabel" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeLabels" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ChangeLabels" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertData" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertData" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertDataWhileScaling" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InsertDataWhileScalingDown" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "GetSpecificKey" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ValidateBasicRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingUpRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "ScalingDownRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "KillPodRedisMasterSlave" "true" "repmaster"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "MasterSlave" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "All" "true" "storage"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteRedKeyCluster"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "InitializeOperator" "false" "false"
# ./test/cmd/redkeyClusterTest.sh "redis-system" "localhost:5001/redis-inditext-operator:v0.2.8" "DeleteAll" "false" "false"
