# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Namespace to deploy to.
# Takes the active namespace. If no namespace active, then `redis-operator` is used by default.
NAMESPACE ?= $(shell kubectl config view --minify --output 'jsonpath={..namespace}'; echo)
ifeq ($(NAMESPACE), )
	NAMESPACE := redis-operator
endif

REDIS_ROBIN=$(shell kubectl -n ${NAMESPACE} get po -l='redis.rediscluster.operator/component=robin' -o=jsonpath='{.items[0].metadata.name}')
IMG_DEV ?= redis-robin:0.0.1

debug:
	# Build the binary with debugging flags
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./main.go
	
	# Wait for the operator pod to be ready (ensure your labels/names are correct)
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l redis.rediscluster.operator/component=robin
	
	# Copy the newly built binary into the operator pod
	kubectl cp ./bin/manager $(REDIS_ROBIN):/manager -n ${NAMESPACE}
	
	# Execute Delve inside the pod so it listens for remote debug connections on port 40001
	kubectl exec -it po/$(REDIS_ROBIN) -n ${NAMESPACE} -- \
		dlv exec /manager \
			--headless \
			--listen=127.0.0.1:40001 \
			--api-version=2 \
			--accept-multiclient \
			--continue

port-forward: ##		Port forwarding of port 40000 for debugging the manager with Delve.
	kubectl port-forward pod/$(REDIS_ROBIN) 40001:40001 -n ${NAMESPACE}


port-forward-metrics: ##		Port forwarding of port 8080 for debugging the manager with Delve.
	kubectl port-forward pod/$(REDIS_ROBIN) 8080:8080 -n ${NAMESPACE}

delete: ##		Delete the operator pod (redis-operator) in order to have a new clean pod created.
	kubectl delete pod $(REDIS_ROBIN) -n ${NAMESPACE}

dev-docker-build:  ##	Build docker image with the robin for development (uses `${IMG_DEV}` image name).
	docker build -t ${IMG_DEV} .

dev-docker-push: ##	Push docker image with the robin for development (uses `${IMG_DEV}` image name).
	docker push ${IMG_DEV}