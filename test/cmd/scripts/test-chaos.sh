# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
set -o errexit
set -o pipefail
set -o nounset

# Determine the directory of this script.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"

REPLICAS="$1"
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"
MINUTES="$5"

# Ensure REPLICAS is greater than 1 (so we can downscale)
if (( REPLICAS <= 1 )); then
    log_error "Invalid input: REPLICAS ($REPLICAS) must be greater than 1."
    exit 1
fi

# Record the start time and calculate the end time (in seconds).
start_time=$(date +%s)
end_time=$(( start_time + MINUTES * 60 ))

log_info "Starting Redis cluster chaos test for cluster '$REDIS_CLUSTER_NAME' in namespace '$NAMESPACE'."

# Ensure Kubernetes namespace exists
if ! ensure_namespace "$NAMESPACE"; then
    log_error "Namespace $NAMESPACE does not exist!"
    exit 1
fi

# Safely kill any running k6 tests; if none are running, log the info
if ! kill_k6; then
    log_info "No k6 tests running"
fi

# Create a clean RedisCluster
if ! create_clean_rdcl "$NAMESPACE" "$LOCAL"; then
    echo "Error: Failed to create RedisCluster in namespace $NAMESPACE"
    exit 1
fi

sleep 2

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch Redis cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

# Wait for Redis to be ready
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Redis cluster failed to become ready."
    exit 1
fi

log_info "Redis Cluster is Ready."


log_info "Launching K6 (${MINUTES} minutes) and patching to ${REPLICAS} replicas."
run_k6_n_minutes "$MINUTES" "$NAMESPACE" "$REDIS_CLUSTER_NAME" &  
K6_PID=$!

sleep 4

# Main loop runs until the current time reaches the end time.
while [ "$(date +%s)" -lt "$end_time" ]; do
    # Generate a random number between 1 and REPLICAS using Bash arithmetic.
    random=$(( RANDOM % REPLICAS + 1 ))
    log_info "LOOPING - Scale Redis - Delete 2 - Wait Ready - delete ${random} random pods."

    
    # Scale Redis cluster
    if ! scale_redis "$REPLICAS" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        echo "Error: Scaling Redis failed"
        exit 1
    fi
    sleep 3  # Allow time for upgrade.
    
    if ! delete_n_random_pods 2 "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Redis cluster is not ready after scaling."
        exit 1
    fi
    sleep 10  # Allow time for upgrade.
    
    # Wait until Redis is ready after scaling
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Redis cluster is not ready after scaling."
        exit 1
    fi
    sleep 5
    
    if ! delete_n_random_pods "$random" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Redis cluster is not ready after scaling."
        exit 1
    fi
    sleep 30
    # Wait until Redis is ready after scaling
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Redis cluster is not ready after scaling."
        exit 1
    fi
    
    # Stop k6 test if still running
    if ps -p "$K6_PID" > /dev/null; then
        log_warning "No k6 process found to kill."
        break
    fi
    
    # Calculate a random downscale value between 1 and (REPLICAS - 1)
    random_downscale=$(( RANDOM % (REPLICAS - 1) + 1 ))
    log_info "Scaling Redis cluster down to ${random_downscale} replicas."
    if ! scale_redis "$random_downscale" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Error: Scaling Redis down failed."
        exit 1
    fi
    
    # Wait until Redis is ready after scaling
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Redis cluster is not ready after scaling."
        exit 1
    fi
done

# Wait until Redis is ready after scaling
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Redis cluster is not ready after scaling."
    exit 1
fi

if ! check_redis "$REDIS_CLUSTER_NAME" "$NAMESPACE"; then
    log_error "Redis cluster is not ready after scaling."
    exit 1
fi
