# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

REPLICAS="${1:-10}"
REDIS_CLUSTER_NAME="${2:-redkey-cluster-test}"
MINUTES="${3:-10}"
LOCAL="${4:-false}"

echo "[INFO] Running tests with REPLICAS=$REPLICAS and CLUSTER_NAME=$REDIS_CLUSTER_NAME, MINUTES=$MINUTES and LOCAL=$LOCAL"

# Get repo root from script location
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

TEST_SCRIPT_DIR="$REPO_ROOT/tests/scripts"
OPERATOR_MANIFEST_DIR="$REPO_ROOT/tests/manifests/deployment-test"
CRD_MANIFEST_DIR="$REPO_ROOT/tests/manifests/crd"
LOG_DIR="$REPO_ROOT/test-logs"
mkdir -p "$LOG_DIR"

if [[ "$LOCAL" == "true" ]]; then
  echo "[INFO] Running tests locally with REPLICAS=$REPLICAS and CLUSTER_NAME=$REDIS_CLUSTER_NAME"
  OPERATOR_MANIFEST_DIR="$REPO_ROOT/tests/manifests/deployment-test-local"
fi

echo "[INFO] Running tests with REPLICAS=$REPLICAS and CLUSTER_NAME=$REDIS_CLUSTER_NAME"
echo "Folder: $TEST_SCRIPT_DIR  $OPERATOR_MANIFEST_DIR"

# Find all test-*.sh files
mapfile -t TEST_FILES < <(find "$TEST_SCRIPT_DIR" -name "test-*.sh" -printf "%f\n")

declare -a pids

for TEST_FILE in "${TEST_FILES[@]}"; do
  NAME="${TEST_FILE%.sh}"
  NAMESPACE="${NAME#test_}"
  # Create a unique log file name for the test output
  TEST_LOG_FILE="$LOG_DIR/${NAMESPACE}-${TEST_FILE}.log"

  (
    set +e
    echo "[TEST: $TEST_FILE] Creating namespace $NAMESPACE"
    kubectl create namespace "$NAMESPACE" > /dev/null 2>&1 || true

    echo "[TEST: $TEST_FILE] Applying CRD manifests in $NAMESPACE"
    kubectl apply -f "$CRD_MANIFEST_DIR" -n "$NAMESPACE"

    echo "[TEST: $TEST_FILE] Applying operator manifests in $NAMESPACE"
    kubectl apply -f "$OPERATOR_MANIFEST_DIR" -n "$NAMESPACE"

    echo "[TEST: $TEST_FILE] Waiting for redkey-operator pod"
    kubectl wait --for=condition=ready --timeout=5m pod -l control-plane=redkey-operator -n "$NAMESPACE"

    echo "[TEST: $TEST_FILE] Running test script (logging to $TEST_LOG_FILE)"
    if "$TEST_SCRIPT_DIR/$TEST_FILE" "$REPLICAS" "$NAMESPACE" "$REDIS_CLUSTER_NAME" "$LOCAL" "$MINUTES" 2>&1 | tee "$TEST_LOG_FILE"; then
      echo "[TEST: $TEST_FILE] ✅ SUCCESS"
    else
      echo "[TEST: $TEST_FILE] ❌ FAILED"
      echo "[TEST: $TEST_FILE] Capturing redkey-operator logs"
      kubectl logs deployment/redkey-operator -n "$NAMESPACE" > "$LOG_DIR/operator-$NAMESPACE.log" 2>&1
    fi

    echo "[TEST: $TEST_FILE] Deleting namespace $NAMESPACE"
    kubectl delete namespace "$NAMESPACE" --wait --ignore-not-found
  ) &
  pids+=($!)
done

# Wait for all tests to finish
for pid in "${pids[@]}"; do
  wait "$pid"
done

echo
echo "========================"
echo " ALL TESTS COMPLETED"
echo "========================"

FAILED_LOGS=$(find "$LOG_DIR" -name "operator-*.log")
if [[ -n "$FAILED_LOGS" ]]; then
  echo "Some tests failed. Logs saved in $LOG_DIR:"
  ls -1 "$LOG_DIR"
  exit 1
else
  echo "All tests passed ✅"
  exit 0
fi
