# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, undefined variables, and fail on pipeline errors
set -o errexit
set -o nounset
set -o pipefail

# Determine the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"

# Read and validate arguments
REPLICAS="$1"
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Ensure namespace exists
if ! ensure_namespace "$NAMESPACE"; then
    echo "Error: Failed to ensure namespace $NAMESPACE"
    exit 1
fi

# Safely kill any running k6 tests; if none are running, log the info
if ! kill_k6; then
    log_info "No k6 tests running"
fi


# Create a clean RedkeyCluster
if ! create_clean_rkcl "$NAMESPACE" "$LOCAL"; then
    echo "Error: Failed to create RedkeyCluster in namespace $NAMESPACE"
    exit 1
fi

sleep 10

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

# Ensure Redis is ready
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    echo "Error: Redis not ready after patch"
    exit 1
fi

log_info "Ready"

log_info "Running k6 test for 120 minutes in the background"
# Run k6 test for 120 minutes in the background
run_k6_n_minutes 120 "$NAMESPACE" "$REDIS_CLUSTER_NAME" &  
K6_PID=$!

sleep 2

# Scale cluster
if ! scale_redis "$REPLICAS" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    echo "Error: Scaling Redis failed"
    exit 1
fi

log_info "Cluster scaled to $REPLICAS replicas."

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

# Wait for Redis to stabilize
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    echo "Error: Redis not ready after scaling"
    exit 1
fi


# Stop k6 test if still running
if ps -p "$K6_PID" > /dev/null; then
    kill_k6 || log_warning "No k6 process found to kill."
fi
