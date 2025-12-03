#!/bin/bash

# Set bash options for better error handling
set -o errexit    # Exit immediately if any command returns a non-zero exit status
set -o nounset    # Treat unset variables as errors
set -o pipefail   # Return a non-zero exit status if any command in a pipeline fails

# Variable declarations
# registry_name='kind-registry'   # Optional: Uncomment and set if a custom registry name is used
registry_port='5001'             # Port to use for the local registry
cluster_name='local-cluster-test'  # Name of the Kind cluster
redis_operator_image=$1          # The name of the Redkey operator image (argument provided during script execution)
host='127.0.0.1'                  # The host address of the local registry

# Function to push the operator image to the local registry and load it into the Kind cluster
push_operator_image_local_registry() {
  echo "Building redkey-operator image..."
  docker pull "${redis_operator_image}"  # Building the Redkey operator image with tag 0.1.0 (assuming Dockerfile is present)

  echo "Pushing redkey-operator image to Kind cluster..."
  docker tag "${redis_operator_image}" "${host}:${registry_port}/${redis_operator_image}"  # Tagging the image for the local registry
  docker push "${host}:${registry_port}/${redis_operator_image}"  # Pushing the image to the local registry

  echo "Image pushed to local registry."

  kind load docker-image "${redis_operator_image}" --name "${cluster_name}"  # Loading the image into the Kind cluster
  echo "Image loaded into Kind cluster."
}

# Call the function to push the operator image to the local registry and load it into Kind cluster
push_operator_image_local_registry