#!/usr/bin/env bash
# Exit on errors
set -o errexit
set -o pipefail
set -o nounset

set +o errexit
set +o pipefail
while sleep 2; do
    ns=$(kubectl get namespace  2> /dev/null  | grep redis | cut -f1 -d" " )
    p=$(kubectl get pod -n $ns 2> /dev/null | grep robin | cut -f1 -d" " 2> /dev/null)
    echo -n "RKCLUST STATUS: "
    kubectl exec $p -n $ns -- curl -s http://localhost:8080/v1/redkeycluster/status 2> /dev/null
    echo
    echo -n "CLUSTER STATUS: "
    kubectl exec $p -n $ns -- curl -s http://localhost:8080/v1/cluster/status 2> /dev/null
    echo
    kubectl get rkcl -n $ns  2> /dev/null
done
