# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# Variable declarations
cluster_name=$1

# Check if kubeconfig file exists
check_kubeconfig() {
  if [ -f "$HOME/.kube/config" ]; then
    echo "Using existing kubeconfig file: $HOME/.kube/config"
  else
    echo "kubeconfig file not found. Creating Kind cluster..."
    check_kind_installed
    create_cluster
  fi
}

# Create Kind cluster with local registry enabled
create_cluster() {
  echo "Creating Kind cluster with local registry enabled in containerd..."
  kind create cluster -n "${cluster_name}" --config=- <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: "$HOME/.docker/config.json"
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
EOF
  echo "Kind cluster with local registry created successfully."
}

# List resources in the default namespace
list_resources() {
  echo "Listing resources in the default namespace..."
  kubectl get all -n default
}

# Check if Kind is installed
check_kind_installed() {
  if command -v kind &>/dev/null; then
    echo "Kind is already installed."
  else
    echo "Kind not found. Installing Kind..."
    install_kind
  fi
}

# Install Kind
install_kind() {
  echo "Installing Kind..."
  local kind_url

  # Determine the correct binary URL based on the system architecture
  case "$(uname -m)" in
    x86_64)
      kind_url="https://kind.sigs.k8s.io/dl/v0.20.0/kind-$(uname -s)-amd64"
      ;;
    aarch64)
      kind_url="https://kind.sigs.k8s.io/dl/v0.20.0/kind-$(uname -s)-arm64"
      ;;
    *)
      echo "Unsupported architecture: $(uname -m)"
      exit 1
      ;;
  esac

  # Download and install Kind
  curl -Lo ./kind "$kind_url"
  chmod +x ./kind
  sudo mv ./kind /usr/local/bin/kind

  echo "Kind installed successfully."
}

# Main script
check_kubeconfig
list_resources