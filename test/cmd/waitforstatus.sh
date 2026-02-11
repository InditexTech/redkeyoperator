# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/bin/bash

# set -e

getStatus()
{
    local namespace=$1
    local name=$2
    local status=$(kubectl get redkeycluster -n $namespace $name --template='{{.status.status}}')
    echo "$status"
}

waitForStatus()
{
    local namespace=$1
    local name=$2
    local waitstatus=$3
    local timeout=$4
    local status=""
    for ((i=1;i<=timeout;i++)); do
        status=$(getStatus $namespace $name)
        if [[ "$status" == "$waitstatus" ]]; then break; fi
        sleep 1s
    done

    # echo "Status=$status"
    if [[ "$waitstatus" != "$status" ]]; then
        # echo "Error getting expected status in RedkeyCluster, Status: $status"
        exit 1
    else
        exit 0
    fi
}

namespace=$1
name=$2
expectedStatus=$3
timeout=$4

# echo "Namespace=$namespace,Name=$name,Status=$status,Timeout=$timeout"
waitForStatus "$namespace" "$name" "$expectedStatus" "$timeout"
