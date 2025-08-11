# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, undefined variables, and pipeline failures
set -o errexit
set -o pipefail
set -o nounset

# Determine the directory of this script and load helper functions from lib-test.sh
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
source "$SCRIPT_DIR/lib-test.sh"

# Assign arguments to variables
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Define the slot number to work with (example: slot 0)
SLOT=0

log_info "Starting Redis cluster test for cluster '$REDIS_CLUSTER_NAME' in namespace '$NAMESPACE'."

# # Start the Redis manager process
# if ! ensure_manager "$NAMESPACE" "$TEST_NAME" ; then
#         log_error "Error: Failed to ensure manager process is running."
#         exit 1
# fi

# Create a clean RedKeyCluster
if ! create_clean_rkcl "$NAMESPACE" "$LOCAL"; then
    log_error "Error: Failed to create RedKeyCluster in namespace $NAMESPACE"
    exit 1
fi

sleep 10

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch Redis cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

log_info "Waiting for Redis Cluster to become ready"

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

log_info "Stopping manager process"

# Stop the Redis manager process before performing slot operations
if ! operator_process_scale "$NAMESPACE" 0; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

# Retrieve node identifiers from pods (using oc command for OpenShift)
log_info "Retrieving node identifiers for slot assignment..."
node0=$(kubectl exec "$REDIS_CLUSTER_NAME-0" -n "$NAMESPACE" -- redis-cli --cluster check localhost 6379 \
    | grep 'M:' | grep 'localhost:6379' | cut -f2 -d" ")
node2=$(kubectl exec "$REDIS_CLUSTER_NAME-2" -n "$NAMESPACE" -- redis-cli --cluster check localhost 6379 \
    | grep 'M:' | grep 'localhost:6379' | cut -f2 -d" ")

log_info "Node identifier from pod '$REDIS_CLUSTER_NAME-0': $node0"
log_info "Node identifier from pod '$REDIS_CLUSTER_NAME-2': $node2"

# Set slot state across the cluster to simulate migration and import
log_info "Setting slot $SLOT states across cluster nodes..."
# Mark pod 0 as migrating to node2
kubectl exec "$REDIS_CLUSTER_NAME-0" -n "$NAMESPACE" -- redis-cli -p 6379 cluster setslot "$SLOT" migrating "$node2"
# Mark pod 2 as importing from node0
kubectl exec "$REDIS_CLUSTER_NAME-2" -n "$NAMESPACE" -- redis-cli -p 6379 cluster setslot "$SLOT" importing "$node0"
# Assign slot to pod 1 with node2 as the owner
kubectl exec "$REDIS_CLUSTER_NAME-1" -n "$NAMESPACE" -- redis-cli -p 6379 cluster setslot "$SLOT" node "$node2"
# Finalize slot assignment on pod 2
kubectl exec "$REDIS_CLUSTER_NAME-2" -n "$NAMESPACE" -- redis-cli -p 6379 cluster setslot "$SLOT" node "$node2"

# Brief pause to allow changes to propagate
sleep 1

if ! operator_process_scale "$NAMESPACE" 1; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

log_info "Redis cluster slot reassignment test completed successfully."
