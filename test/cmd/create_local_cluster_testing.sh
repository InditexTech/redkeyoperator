#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# Variable declarations
registry_name='kind-registry'
registry_port='5001'
cluster_name='local-cluster-test'
redis_operator_name=${1}
redis_operator_version='0.1.0'
host='127.0.0.1'

# Check if kubeconfig file exists
check_kubeconfig() {
  if [ -f "$HOME/.kube/config" ]; then
    echo "Using existing kubeconfig file: $HOME/.kube/config"
  else
    echo "kubeconfig file not found. Creating Kind cluster..."
    check_kind_installed
    create_cluster_with_local_registry
  fi
}

# Create cluster with local registry
create_cluster_with_local_registry() {
  create_local_registry
  create_cluster
  add_registry_to_nodes
  connect_registry_to_cluster_network
  document_local_registry
  push_operator_image_local_registry
  add_images_for_testing
}

# Create local Docker registry
create_local_registry() {
  echo "Creating local Docker registry..."
  if ! docker inspect -f '{{.State.Running}}' "${registry_name}" >/dev/null 2>&1; then
    docker run -d --restart=always -p "127.0.0.1:${registry_port}:5000" --name "${registry_name}" registry:2
  fi
  echo "Local Docker registry created successfully."
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

# Add local registry to nodes
add_registry_to_nodes() {
  local registry_dir="/etc/containerd/certs.d/${host}:${registry_port}"
  for node in $(kind get nodes); do
    docker exec "${node}" mkdir -p "${registry_dir}"
    cat <<EOF | docker exec -i "${node}" cp /dev/stdin "${registry_dir}/hosts.toml"
[host."http://${registry_name}:5000"]
EOF
  done
}

connect_registry_to_cluster_network() {
  echo "Connecting the registry to the cluster network if not already connected..."
  if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${registry_name}")" = 'null' ]; then
    docker network connect "kind" "${registry_name}"
  fi
  echo "Registry connected to the cluster network."
}

# Push operator image to local registry
push_operator_image_local_registry() {
  echo "Building redis-operator image... ${redis_operator_name}:${redis_operator_version}"
  docker build -t "${redis_operator_name}:${redis_operator_version}" .
  echo "Pushing redis-operator image to Kind cluster..."
  docker tag "${redis_operator_name}:${redis_operator_version}" "${host}:${registry_port}/${redis_operator_name}:${redis_operator_version}"
  docker push "${host}:${registry_port}/${redis_operator_name}:${redis_operator_version}"
  echo "Image pushed to local registry."
  kind load docker-image "${redis_operator_name}:${redis_operator_version}" --name "${cluster_name}"
  echo "Image loaded into Kind cluster."

}
add_images_for_testing() {
  docker pull redis/redis-stack-server:7.2.0-v10
  echo "Image pulled redis/redis-stack-server:7.2.0-v10."
  kind load docker-image redis/redis-stack-server:7.2.0-v10 --name "${cluster_name}"
  echo "Image loaded redis-stack-server image into Kind cluster."

  docker pull redis/redis-stack-server:7.4.0-v3
  echo "Image pulled redis/redis-stack-server:7.4.0-v3."
  kind load docker-image redis/redis-stack-server:7.4.0-v3 --name "${cluster_name}"
  echo "Image loaded redis-stack-server image into Kind cluster."

  docker pull alpine:3.21.3
  echo "Image pulled alpine:3.21.3."
  kind load docker-image alpine:3.21.3 --name "${cluster_name}"
  echo "Image loaded redis-stack-server image into Kind cluster."
}

# Document the local registry
document_local_registry() {
  echo "Documenting the local registry..."
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "${host}:${registry_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
  echo "Local registry documentation applied."
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