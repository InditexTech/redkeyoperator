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

log_info "Starting Redis resource assertions test for cluster '$REDIS_CLUSTER_NAME' in namespace '$NAMESPACE'."

# # Start manager process and create a clean RedkeyCluster
# if ! ensure_manager "$NAMESPACE" "$TEST_NAME" ; then
#     log_error "Error: Failed to ensure manager process is running."
#     exit 1
# fi

if ! create_clean_rkcl "$NAMESPACE" "$LOCAL"; then
    log_error "Error: Failed to create RedkeyCluster in namespace '$NAMESPACE'."
    exit 1
fi

sleep 2

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
fi

log_info "Waiting for Cluster to become ready..."
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Cluster is not ready."
    exit 1
fi

# Assert initial resource settings
assert_cpu "50m" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"
assert_memory "100Mi" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"
assert_maxmemory "1677721600" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"

# Apply manifest to update resource settings
log_info "Applying updated resource settings manifest..."

if [[ "$LOCAL" == "true" ]]; then
    if ! kubectl apply -n "$NAMESPACE" -f hack/tests/manifests/rkcl-test-size-local.yml; then
        log_error "Error: Failed to apply updated resource settings manifest."
        exit 1
    fi
else
    # Apply Loki logging configuration
    if ! kubectl apply -n "$NAMESPACE" -f hack/tests/manifests/rkcl-test-size.yml; then
        log_error "Error: Failed to apply updated resource settings manifest."
        exit 1
    fi
fi

# Wait for new resource values to propagate
sleep 2
log_info "Waiting for updated resource values to take effect..."

if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Error: Cluster is not ready after resource update."
    exit 1
fi

# Assert updated resource settings
assert_cpu "100m" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"
assert_memory "128Mi" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"
assert_maxmemory "94371840" "${REDIS_CLUSTER_NAME}" "${NAMESPACE}"

log_info "Redis resource assertions test completed successfully."
