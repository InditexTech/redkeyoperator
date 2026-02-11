# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, pipeline failures, and unset variables
set -o errexit
set -o pipefail
set -o nounset

# Determine the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"

REPLICAS="$1"
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Ensure Kubernetes namespace exists
if ! ensure_namespace "$NAMESPACE"; then
    log_error "Namespace $NAMESPACE does not exist!"
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

sleep 2

if [[ "$LOCAL" == "false" ]]; then
    if ! patch_statefulset "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Patch cluster StatefulSet"
        exit 1
    fi
    log_info "Patched StatefulSet/$REDIS_CLUSTER_NAME security context."
fi

# Wait for Redis to be ready
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Cluster failed to become ready."
    exit 1
fi

log_info "Cluster is Ready."

# Scale cluster
if ! scale_redis "$REPLICAS" "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    echo "Error: Cluster scaling failed"
    exit 1
fi

log_info "Scaled Cluster to $REPLICAS replicas."
sleep 10  # Allow time for scaling

# Wait until Redis is ready after scaling
if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
    log_error "Cluster is not ready after scaling."
    exit 1
fi

# Generate random pod selection for deletion
NUM_TO_DELETE=$((REPLICAS / 2))
mapfile -t AVAILABLE_PODS < <(seq 0 $((REPLICAS - 1)) | shuf | head -n "$NUM_TO_DELETE")

# Construct pod list for deletion
PODS_TO_DELETE=()
for pod in "${AVAILABLE_PODS[@]}"; do
    PODS_TO_DELETE+=("$REDIS_CLUSTER_NAME-$pod")
done

# Handle Pod Deletion
if [[ ${#PODS_TO_DELETE[@]} -gt 0 ]]; then
    log_info "Deleting random pods: ${PODS_TO_DELETE[*]}"

    # Ensure the pods exist before deleting
    for pod in "${PODS_TO_DELETE[@]}"; do
        if ! kubectl get pod "$pod" -n "$NAMESPACE" &>/dev/null; then
            log_warning "Pod $pod does not exist, skipping..."
        else
            kubectl delete pod "$pod" -n "$NAMESPACE"
        fi
    done

    log_info "Waiting for Cluster to recover from pod deletion..."
    sleep 30  # Allow Kubernetes to recognize deleted pods
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Cluster did not recover after pod deletion."
        exit 1
    fi
fi

log_info "Test execution completed successfully."