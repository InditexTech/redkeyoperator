# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash
# Exit on errors, pipeline failures, and unset variables
set -o errexit
set -o pipefail
set -o nounset

# Determine the script directory and source helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib-test.sh"  # Expects functions: ensure_namespace, kill_k6, create_clean_rkcl, wait_redis_ready, log_info, log_error

# Read and assign arguments: $2 for namespace and $3 for cluster name; ignore $1 (replicas)
NAMESPACE="$2"
REDIS_CLUSTER_NAME="$3"
LOCAL="${4:-false}"

# Function: Retrieve pod JSON once and validate both log format annotation and log scrape label
check_pod_properties() {
    local expected_log_format="$1"
    local expected_log_scrape="$2"

    # Retrieve the pod JSON for the first pod in the cluster
    local pod_json
    if ! pod_json=$(kubectl get pod/"$REDIS_CLUSTER_NAME"-0 -n "$NAMESPACE" -o json); then
        log_error "Failed to retrieve pod information for $REDIS_CLUSTER_NAME-0 in namespace $NAMESPACE"
        exit 1
    fi

    # Extract the current log format annotation and log scrape label using jq
    local actual_log_format
    actual_log_format=$(jq -r '.metadata.annotations["log-format"] // ""' <<< "$pod_json")
    local actual_log_scrape
    actual_log_scrape=$(jq -r '.metadata.labels["log-scrape"] // ""' <<< "$pod_json")

    if [[ "$actual_log_format" != "$expected_log_format" ]]; then
        log_error "Assertion failed: Expected log format '$expected_log_format', but found '$actual_log_format'."
        exit 1
    fi

    if [[ "$actual_log_scrape" != "$expected_log_scrape" ]]; then
        log_error "Assertion failed: Expected log scrape '$expected_log_scrape', but found '$actual_log_scrape'."
        exit 1
    fi
}

# Main function encapsulating the script's workflow
main() {
    # Ensure the namespace exists
    if ! ensure_namespace "$NAMESPACE"; then
        log_error "Error: Failed to ensure namespace $NAMESPACE"
        exit 1
    fi

    # Safely kill any running k6 tests; if none are running, log the info
    if ! kill_k6; then
        log_info "No k6 tests running"
    fi

    # Create a clean RedkeyCluster
    if ! create_clean_rkcl "$NAMESPACE" "$LOCAL"; then
        log_error "Error: Failed to create RedkeyCluster in namespace $NAMESPACE"
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

    # Wait for the Cluster to be ready (expected version: 4.0.0)
    log_info "Waiting for Cluster to become ready (expected version: 4.0.0)..."
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Error: Redis not ready after patch"
        exit 1
    fi

    # Validate initial log format and scrape label (expected empty values)
    check_pod_properties "" ""

    if [[ "$LOCAL" == "true" ]]; then
        if ! kubectl apply -n "$NAMESPACE" -f hack/tests/manifests/rkcl-test-loki-local.yml; then
            log_error "Error: Failed to apply Loki logging configuration"
            exit 1
        fi
    else
        # Apply Loki logging configuration
        if ! kubectl apply -n "$NAMESPACE" -f hack/tests/manifests/rkcl-test-loki.yml; then
            log_error "Error: Failed to apply Loki logging configuration"
            exit 1
        fi
    fi

    sleep 5

    log_info "Applied logging configuration, waiting for Cluster to become ready (expected version: 4.0.1)..."

    # Wait for readiness after applying logging configuration
    if ! wait_redis_ready "$NAMESPACE" "$REDIS_CLUSTER_NAME"; then
        log_error "Error: Cluster is not ready after applying Loki configuration"
        exit 1
    fi

    # Validate updated log format and scrape label (expected values: "lineformat" and "true")
    check_pod_properties "lineformat" "true"

    log_info "All assertions passed successfully!"
}

# Execute the main function
main
