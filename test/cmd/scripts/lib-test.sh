# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/bin/bash

# Global variable for cluster name
REDIS_CLUSTER_NAME="redis-cluster-test"

# Function: Print usage information and exit
function usage() {
    echo "Usage: $0 <replicas> <namespace> <redkey-cluster-name>" >&2
    exit 1
}


function delete_n_random_pods() {
    # Validate required arguments.
    if [[ $# -lt 3 ]]; then
        echo "Usage: ${FUNCNAME[0]} <count> <namespace> <cluster>" >&2
        return 1
    fi

    local count="$1"
    local namespace="$2"
    local cluster="$3"

    # Validate that count is a positive integer.
    if ! [[ "$count" =~ ^[0-9]+$ ]] || [[ "$count" -eq 0 ]]; then
        echo "Error: <count> must be a positive integer." >&2
        return 1
    fi

    # Retrieve pod names matching the cluster pattern.
    # --no-headers avoids including the table header from kubectl.
    local pods=()
    while IFS= read -r pod; do
        pods+=("$pod")
    done < <(kubectl get pods -n "$namespace" --no-headers \
             | awk -v cluster="$cluster" '$1 ~ "^"cluster"-" { print $1 }' | shuf)

    if [[ ${#pods[@]} -eq 0 ]]; then
        log_error "No Redis pods found to delete in namespace $namespace."
        return 1
    fi

    # Select up to the requested number of pods.
    local pods_to_delete=("${pods[@]:0:$count}")

    log_info "Deleting the following pods: ${pods_to_delete[@]}"

    # Delete the selected pods.
    local delete_output
    if ! delete_output=$(kubectl delete pod -n "$namespace" "${pods_to_delete[@]}"); then
        log_error "Failed to delete some pods in namespace $namespace."
        return 1
    fi

    log_info "Deleted pods: $delete_output"

    # Count the deleted pods from the output.
    local deleted_count
    deleted_count=$(echo "$delete_output" | grep -c "deleted" || echo "0")

    log_info "Deleted ${deleted_count} pod(s) (requested: ${count}) in namespace ${namespace}."
}

# Function to run k6 performance test for a given number of minutes.
# Usage: run_k6_n_minutes <duration_minutes> <namespace> <cluster>
function run_k6_n_minutes() {
    # Validate required arguments.
    if [[ $# -lt 3 ]]; then
        log_error "Usage: ${FUNCNAME[0]} <duration_minutes> <namespace> <cluster>"
        return 1
    fi

    local duration="$1"
    local namespace="$2"
    local cluster="$3"

    # Validate that duration is a positive integer.
    if ! [[ "$duration" =~ ^[1-9][0-9]*$ ]]; then
        log_error "Duration must be a positive integer (in minutes)."
        return 1
    fi

    # Determine the Redis nodes by querying the specified namespace.
    # This command assumes pod names include the cluster identifier and that the target IP is in the (NF-3) column.
    local nodes
    nodes=$(kubectl get pods -n "$namespace" -o wide --no-headers \
             | awk -v cluster="$cluster" '$1 ~ "^"cluster { print $(NF-3) ":6379" }' \
             | paste -sd "," -)
    if [[ -z "$nodes" ]]; then
        log_error "No Redis nodes found in namespace $namespace! Exiting..."
        return 1
    fi

    log_info "Running k6 test with REDIS_HOSTS: $nodes"
    export REDIS_HOSTS="$nodes"

    # Compute the project root. This assumes the script is executed from the project root.
    local project_root
    project_root="$(pwd)"

    # Compute the absolute paths to the k6 binary and the k6 script.
    local k6_bin="${project_root}/bin/k6"
    local k6_script_path="${project_root}/hack/tests/k6scripts/test-300k.js"

    # Check if the k6 binary exists.
    if [[ ! -x "$k6_bin" ]]; then
        log_error "k6 binary not found at ${k6_bin}"
        return 1
    fi

    log_info "Using k6 script: ${k6_script_path}"

    # Run k6 using the absolute paths.
    "$k6_bin" run "${k6_script_path}" --duration "${duration}m" --vus 10 > k6.log 2>&1 &
    log_info "k6 test started in background. Check k6.log for details."
}


# Function to get the PID(s) of running k6 test.
function pid_k6 {
    local pids
    pids=$(pgrep -f "k6.*test-300k")
    echo "$pids"
}

# Function to stop k6 test if running.
function kill_k6 {
    local pid
    pid=$(pid_k6)
    
    if [[ -n "$pid" ]]; then
        log_info "Stopping k6 test process: $pid"
        if kill $pid; then
            log_info "Successfully killed k6 process: $pid"
        else
            log_error "Failed to kill one or more k6 process: $pid"
        fi
    else
        log_info "No running k6 test process found."
    fi
    rm -f k6.log
}

# Function to wait until cluster is ready, with a timeout to avoid infinite waiting.
function wait_redis_ready {
    local namespace="$1"
    local cluster="$2"
    local delay="${3:-2}"
    local timeout="${4:-6000}"  # Maximum wait time in seconds

    log_info "Waiting for Cluster '$cluster' in namespace '$namespace' to be ready (timeout: ${timeout}s)..."

    local start_time current_time elapsed last_status=""
    start_time=$(date +%s)

    while true; do
        current_time=$(date +%s)
        elapsed=$(( current_time - start_time ))
        if (( elapsed >= timeout )); then
            log_error "Timeout reached: Cluster '$cluster' in namespace '$namespace' did not become ready after ${timeout}s."
            return 1
        fi

        # Fetch the cluster JSON once.
        local cluster_json
        if ! cluster_json=$(kubectl get rediscluster/"$cluster" -n "$namespace" -o json 2>/dev/null); then
            log_error "Failed to retrieve cluster JSON in namespace '$namespace'. Retrying..."
            sleep "$delay"
            continue
        fi

        # Extract desired fields from the JSON.
        local replicas status
        replicas=$(echo "$cluster_json" | jq -r '.spec.replicas // empty')
        status=$(echo "$cluster_json" | jq -r '.status.status // empty')

        if [[ -z "$replicas" || -z "$status" ]]; then
            log_error "Incomplete cluster information in namespace '$namespace'. Retrying..."
            sleep "$delay"
            continue
        fi

        if [[ "$status" != "Ready" ]]; then
            # Log status only if it has changed.
            if [[ "$status" != "$last_status" ]]; then
                log_info "cluster status is '$status'. Waiting..."
                last_status="$status"
            fi
            sleep "$delay"
            continue
        fi

        # Get pod names in one command.
        local redis_pods
        redis_pods=$(kubectl get pods -n "$namespace" -l deployment="$cluster" -o jsonpath='{.items[*].metadata.name}')
        if [[ -z "$redis_pods" ]]; then
            log_error "No pods found in namespace '$namespace'. Retrying..."
            sleep "$delay"
            continue
        fi

        local all_ready=true
        for pod in $redis_pods; do
            # Check cluster state inside each pod.
            if ! kubectl exec -n "$namespace" "$pod" -- redis-cli --cluster check localhost 6379 &>/dev/null; then
                log_error "Cluster check failed for pod '$pod'. Retrying..."
                all_ready=false
                break
            fi

            # Ensure the cluster topology does not show unhealthy state.
            if kubectl exec -n "$namespace" "$pod" -- redis-cli cluster nodes | grep -E '(fail|->)' &>/dev/null; then
                log_error "Cluster nodes unhealthy in pod '$pod'. Retrying..."
                all_ready=false
                break
            fi
        done

        if [[ "$all_ready" == "true" ]]; then
            log_info "Cluster is Ready!"
            return 0
        fi

        sleep "$delay"
    done
}


# Function to scale cluster.
function scale_redis {
    local replicas="$1"
    local namespace="$2"
    local cluster="$3"

    # Validate input arguments.
    if [[ -z "$replicas" || -z "$namespace" || -z "$cluster" ]]; then
        log_error "Missing arguments for scale_redis. Expected: replicas, namespace, and cluster."
        return 1
    fi

    log_info "Scaling cluster '$cluster' to $replicas replicas in namespace '$namespace'."

    if ! kubectl patch rediscluster/"$cluster" -n "$namespace" -p '{"spec":{"replicas":'"$replicas"'}}' --type merge; then
        log_error "Failed to scale cluster."
        return 1
    fi

    log_info "Successfully scaled cluster '$cluster' to $replicas replicas in namespace '$namespace'."
}


# Logging functions
function log_info {
    echo "$(date +"%Y/%m/%d %H:%M:%S") INFO: $*"
}

function log_error {
    echo "$(date +"%Y/%m/%d %H:%M:%S") ERROR: $*" >&2
}

function log_warning {
    echo "$(date +"%Y/%m/%d %H:%M:%S") WARNING: $*" >&2
}

# Function to create a clean RedisCluster
function create_clean_rkcl {
    local namespace="$1"
    local is_local="${2:-false}"

    local manifest

    # Select the appropriate manifest file based on the environment.
    if [[ "$is_local" == "true" ]]; then
        manifest="hack/tests/manifests/rkcl-test-local.yml"
    else
        manifest="hack/tests/manifests/rkcl-test.yml"
    fi

    # Delete the resource using --ignore-not-found for clarity.
    if ! kubectl delete -n "$namespace" -f "$manifest" --ignore-not-found; then
        echo "Warning: Failed to delete resources in namespace '$namespace' using manifest '$manifest'" >&2
    fi

    # Wait briefly for cleanup (consider replacing with kubectl wait if available).
    sleep 2

    # Apply the manifest.
    kubectl apply -n "$namespace" -f "$manifest"
}

# Function to check if RedisCluster CRD exists
function ensure_rediscluster {
    if ! kubectl explain rediscluster &>/dev/null; then
        log_error "Kubernetes or RedisCluster CRD is missing!"
        return 1
    fi
}

# Function to ensure a namespace exists
function ensure_namespace {
    local namespace="$1"

    if ! kubectl get namespace "$namespace" &>/dev/null; then
        log_error "Namespace $namespace is missing!"
        return 1
    fi
}

# Function to ensure the Redis manager process is running.
function ensure_manager {
    local namespace="$1"
    local test_name="$2"
    
    # Compute the project root. This assumes you run the script from the project root.
    local project_root
    project_root="$(pwd)"
    
    # Compute the absolute path to the manager binary in the project's bin folder.
    local manager_bin="${project_root}/bin/manager"
    
    # If the manager binary is not found or is not executable, build it.
    if [ ! -x "${manager_bin}" ]; then
        log_info "Manager binary not found at ${manager_bin}. Building executable..."
        # Build the manager binary with the desired flags.
        go build -ldflags "${GO_COMPILE_FLAGS:-}" -tags release -o "${manager_bin}" "${project_root}"/cmd/manager/main.go
        chmod +x "${manager_bin}"
    fi

    # Check if the manager process is already running.
    if ! pgrep -f "${manager_bin}" > /dev/null; then
        rm -rf manager.log
        WATCH_NAMESPACE="$namespace" nohup "${manager_bin}" --max-concurrent-reconciles 10 > manager_"${test_name}".log 2>&1 &
    fi
}

# Function to stop the Redis manager process
function stop_manager {
    local pid
    pid=$(pgrep -f '../bin/manager')

    if [[ -n "$pid" ]]; then
        kill "$pid"
    fi
}

# Function to scale operator to shutdown
function operator_process_scale {
    # Validate required arguments
    if [[ $# -lt 2 ]]; then
        echo "Usage: ${FUNCNAME[0]} <namespace> <replicas (0 or 1)>" >&2
        return 1
    fi

    local namespace="$1"
    local replicas="$2"
    local operator="redis-operator"

    # Ensure replicas is either 0 or 1
    if [[ "$replicas" -ne 0 && "$replicas" -ne 1 ]]; then
        echo "Invalid replicas value: $replicas. Only 0 or 1 is allowed." >&2
        return 1
    fi

    # Patch the deployment to the desired replica count
    if ! kubectl patch deployment "$operator" -n "$namespace" --type='merge' \
        -p '{"spec":{"replicas": '"$replicas"'}}'; then
        echo "Failed to patch deployment '$operator'  in namespace '$namespace'." >&2
        return 1
    fi

    if [[ "$replicas" -eq 0 ]]; then
        # Wait for the operator to scale down
        if ! kubectl wait --for=delete --timeout=5m pod -l control-plane=redis-operator -n "$namespace"; then
            echo "Failed to wait for operator pod deletion in namespace '$namespace'." >&2
            return 1
        fi
    else 
        # Wait for the operator to scale up
        if ! kubectl wait --for=condition=ready --timeout=5m pod -l control-plane=redis-operator -n "$namespace"; then
            echo "Failed to wait for operator pod readiness in namespace '$namespace'." >&2
            return 1
        fi
    fi
}


# Function to check cluster status
function check_redis {
    local cluster="$1"
    local namespace="$2"

    local replicas status
    replicas=$(kubectl get rediscluster/"$cluster" -n "$namespace" -o json | jq -r .spec.replicas)
    status=$(kubectl get rediscluster/"$cluster" -n "$namespace" -o json | jq -r .status.status)

    if [[ "$status" != "Ready" ]]; then
        log_error "cluster $cluster is not Ready in namespace $namespace (Status: $status)"
        return 1
    fi

    for i in $(seq 0 $(( replicas - 1 )) ); do
        if ! kubectl exec -n "$namespace" "$cluster-$i" -- redis-cli --cluster check localhost 6379 &>/dev/null; then
            log_error "Cluster check failed for pod $cluster-$i in namespace $namespace."
            return 1
        fi
    done

    log_info "OK - Cluster: $cluster - Replicas: $replicas in namespace $namespace."
}

# Function to patch the StatefulSet to enforce non-root execution
function patch_statefulset {
    local namespace="$1"
    local cluster="$2"

    kubectl patch sts "$cluster" -n "$namespace" --type='merge' \
    -p '{"spec":{"template":{"spec":{"securityContext":{"runAsNonRoot": true, "runAsUser": 1000}}}}}'
}

# Assertion functions using dynamic pod names and namespace

function assert_maxmemory {
    local expected="$1"
    local cluster="$2"
    local namespace="$3"
    local v
    v=$(kubectl exec "${cluster}-0" -n "${namespace}" -- redis-cli config get maxmemory | tail -n 1)
    if [[ "$v" != "$expected" ]]; then
        log_error "Maxmemory assertion failed: expected '$expected', found '$v'."
        exit 1
    fi
}

function assert_cpu {
    local expected="$1"
    local cluster="$2"
    local namespace="$3"
    local v
    v=$(kubectl get pod "${cluster}-0" -n "${namespace}" -o json | jq -r '.spec.containers[0].resources.limits.cpu')
    if [[ "$v" != "$expected" ]]; then
        log_error "CPU assertion failed: expected '$expected', found '$v'."
        exit 1
    fi
}

function assert_memory {
    local expected="$1"
    local cluster="$2"
    local namespace="$3"
    local v
    v=$(kubectl get pod "${cluster}-0" -n "${namespace}" -o json | jq -r '.spec.containers[0].resources.limits.memory')
    if [[ "$v" != "$expected" ]]; then
         log_error "Memory assertion failed: expected '$expected', found '$v'."
         exit 1
    fi
}

function assert_scrape {
    local expected="$1"
    local cluster="$2"
    local namespace="$3"
    local v
    v=$(kubectl get pod "${cluster}-0" -n "${namespace}" -o json | jq -r '.metadata.labels["log-scrape"] // ""')
    if [[ "$v" != "$expected" ]]; then
        log_error "Log scrape assertion failed: expected '$expected', found '$v'."
        exit 1
    fi
}