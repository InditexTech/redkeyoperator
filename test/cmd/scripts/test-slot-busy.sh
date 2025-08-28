# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, undefined variables, and pipeline failures
set -o errexit
set -o pipefail
set -o nounset

# Determine the directory of this script and load helper functions from lib-test.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"

# Assign arguments to variables
REPLICAS="$1"
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Define the slot number to work with (example: slot 0)
SLOT=0

# Ensure namespace exists
if ! ensure_namespace "$NAMESPACE"; then
    echo "Error: Failed to ensure namespace $NAMESPACE"
    exit 1
fi

log_info "Starting cluster scale test for cluster '$REDIS_CLUSTER_NAME' in namespace '$NAMESPACE' with $REPLICAS replicas."

# # Start manager process and create a clean RedisCluster
# if ! ensure_manager "$NAMESPACE" "$TEST_NAME" ; then
#     log_error "Error: Failed to ensure manager process is running."
#     exit 1
# fi

# Create a clean RedisCluster
if ! create_clean_rdcl "$NAMESPACE" "$LOCAL"; then
    echo "Error: Failed to create RedisCluster in namespace $NAMESPACE"
    exit 1
fi
sleep 2

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

# Scale the cluster
log_info "Scaling cluster '$REDIS_CLUSTER_NAME' to $REPLICAS replicas..."

# Scale cluster
if ! scale_redis "$REPLICAS" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    echo "Error: Cluster scaling failed"
    exit 1
fi

sleep 2

log_info "Cluster scaled to $REPLICAS replicas."

log_info "Waiting for $REPLICAS replicas to become ready..."

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

# Stop the Redis manager process before slot operations
log_info "Stopping operator process..."
if ! operator_process_scale "$NAMESPACE" 0; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

# Retrieve node identifiers for each pod and store in an array
declare -a nodes
for i in $(seq 0 $(( REPLICAS - 1 )) ); do
    node=$(kubectl exec "${REDIS_CLUSTER_NAME}-$i" -n "$NAMESPACE" -- redis-cli --cluster check localhost 6379 \
        | grep 'M:' | grep 'localhost:6379' | cut -f2 -d" ")
    nodes[$i]="$node"
    log_info "Node identifier for pod '${REDIS_CLUSTER_NAME}-$i': ${nodes[$i]}"
done

# Remove slot $SLOT from all nodes
log_info "Removing slot $SLOT from all nodes..."
for i in $(seq 0 $(( REPLICAS - 1 )) ); do
    kubectl exec "${REDIS_CLUSTER_NAME}-$i" -n "$NAMESPACE" -- redis-cli -p 6379 cluster delslots "$SLOT"
done

# Assign slot $SLOT to a new node on each target pod
log_info "Assigning slot $SLOT to new node targets..."
for i in $(seq 0 $(( REPLICAS - 1 )) ); do
    # Determine the target pod index (rotate by +1 modulo REPLICAS)
    target_index=$(( (i + 1) % REPLICAS ))
    kubectl exec "${REDIS_CLUSTER_NAME}-$target_index" -n "$NAMESPACE" -- redis-cli -p 6379 cluster setslot "$SLOT" node "${nodes[$i]}"
    log_info "Assigned slot $SLOT on pod '${REDIS_CLUSTER_NAME}-$target_index' to node '${nodes[$i]}'."
done

if ! operator_process_scale "$NAMESPACE" 1; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

log_info "Cluster scaling and slot reassignment test completed successfully."
