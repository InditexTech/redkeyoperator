#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 INDUSTRIA DE DISENO TEXTIL S.A. (INDITEX S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Exit on errors
set -o errexit
set -o pipefail
set -o nounset

set +o errexit
set +o pipefail

if (( $# > 0 )); then
    nss=$1
else
    nss=$(kubectl get namespace  2> /dev/null  | grep -E '(redis|chaos)' | cut -f1 -d" " )
fi
for ns in $nss; do
    p=$(kubectl get pod -n $ns 2> /dev/null | grep robin | cut -f1 -d" " 2> /dev/null)
    echo -n "RKCLUST STATUS: "
    kubectl exec $p -n $ns -- curl -s http://localhost:8080/v1/redkeycluster/status 2> /dev/null
    echo
    echo -n "CLUSTER STATUS: "
    kubectl exec $p -n $ns -- curl -s http://localhost:8080/v1/cluster/status 2> /dev/null
    echo
    kubectl get rkcl -n $ns  2> /dev/null
done
