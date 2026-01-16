# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/bin/bash

set -euo pipefail

cluster_name='local-cluster-test'
kind_cluster_name=$(kubectl config view --minify --output 'jsonpath={.clusters[0].name}')
# Function to delete the Kind cluster
delete_cluster() {
  echo "Deleting Kind cluster..."
  kind delete cluster -n local-cluster-test
  echo "Kind cluster deleted successfully."
}

# Function to check if the kubeconfig file belongs to Kind
is_kind_kubeconfig() {
  local kubeconfig_path="${HOME}/.kube/config"
  if [ -f "$kubeconfig_path" ]; then
    if [ "kind-$cluster_name" = "$kind_cluster_name" ]; then
      return 0
    fi
  fi
  return 1
}

# Function to remove the kubeconfig file if it belongs to Kind
remove_kubeconfig() {
  echo "Checking if kubeconfig file belongs to Kind..."
  if is_kind_kubeconfig; then
    echo "Kubeconfig file belongs to Kind. Removing kubeconfig..."
    rm "$HOME/.kube/config"
    echo "Kubeconfig removed successfully."
  else
    echo "Kubeconfig file does not belong to Kind. Skipping kubeconfig removal."
  fi
}

# Main script
delete_cluster
remove_kubeconfig