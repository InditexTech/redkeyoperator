# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, undefined variables, and pipeline failures
set -o errexit
set -o pipefail
set -o nounset

# Assign arguments to variables
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Determine the directory of this script and load helper functions from lib-test.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"

log_info "Starting cluster test for cluster '$REDIS_CLUSTER_NAME' in namespace '$NAMESPACE'."

# # Restart the Redis manager process and wait for the cluster to become ready again
# if ! ensure_manager "$NAMESPACE" "$TEST_NAME" ; then
#     log_error "Error: Failed to ensure manager process is running."
#     exit 1
# fi

# Create a clean RedkeyCluster
if ! create_clean_rkcl "$NAMESPACE" "$LOCAL"; then
    echo "Error: Failed to create RedkeyCluster in namespace $NAMESPACE"
    exit 1
fi

sleep 5

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

log_info "Waiting for replicas to become ready..."

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

# Stop the Redis manager process before slot operations
log_info "Stopping manager process..."
if ! operator_process_scale "$NAMESPACE" 0; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

# Retrieve node identifiers from pods 0 and 2 using the oc command
log_info "Retrieving node identifiers for pods '${REDIS_CLUSTER_NAME}-0' and '${REDIS_CLUSTER_NAME}-2'..."
node0=$(kubectl exec "${REDIS_CLUSTER_NAME}-0" -n "$NAMESPACE" -- redis-cli --cluster check localhost 6379 \
    | grep "M:" | grep "localhost:6379" | cut -f2 -d" ")
node2=$(kubectl exec "${REDIS_CLUSTER_NAME}-2" -n "$NAMESPACE" -- redis-cli --cluster check localhost 6379 \
    | grep "M:" | grep "localhost:6379" | cut -f2 -d" ")

log_info "Node identifier from pod '${REDIS_CLUSTER_NAME}-0': $node0"
log_info "Node identifier from pod '${REDIS_CLUSTER_NAME}-2': $node2"

# Remove a range of slots from pod 0
log_info "Removing slots range 0-5461 from pod '${REDIS_CLUSTER_NAME}-0'..."
kubectl exec "${REDIS_CLUSTER_NAME}-0" -n "$NAMESPACE" -- redis-cli cluster delslotsrange 0 5461

# Instruct pod 0 to replicate the node identified by node2
log_info "Setting pod '${REDIS_CLUSTER_NAME}-0' to replicate node '$node2'..."
kubectl exec "${REDIS_CLUSTER_NAME}-0" -n "$NAMESPACE" -- redis-cli cluster replicate "$node2"

if ! operator_process_scale "$NAMESPACE" 1; then
    log_error "Error: Failed to stop manager process."
    exit 1
fi

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Redis not ready after patch"
    exit 1
fi

log_info "Cluster slot reassignment test completed successfully."
