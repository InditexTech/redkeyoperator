#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 INDUSTRIA DE DISENO TEXTIL S.A. (INDITEX S.A.)
#
# SPDX-License-Identifier: Apache-2.0

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
