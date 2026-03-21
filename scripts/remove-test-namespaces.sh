#!/usr/bin/env bash
# Exit on errors
set -o errexit
set -o pipefail
set -o nounset

# scripts directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

for i in $(kubectl get ns -o json | jq -r '.items[]| select(.metadata.name | test("redkey-e2e|chaos")) |.metadata.name'); do
    $SCRIPT_DIR/finalizers-clean.sh -y -n $i &
    kubectl delete ns/$i &
done
