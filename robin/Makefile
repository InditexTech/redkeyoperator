# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Namespace to deploy to.
# Takes the active namespace. If no namespace active, then `redis-operator` is used by default.
NAMESPACE ?= $(shell kubectl config view --minify --output 'jsonpath={..namespace}'; echo)
ifeq ($(NAMESPACE), )
	NAMESPACE := redis-operator
endif

REDIS_MONITORING=$(shell kubectl -n ${NAMESPACE} get po -l='redis.rediscluster.operator/component=monitoring' -o=jsonpath='{.items[0].metadata.name}')

debug:
	# Build the binary with debugging flags
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./main.go
	
	# Wait for the operator pod to be ready (ensure your labels/names are correct)
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l redis.rediscluster.operator/component=monitoring
	
	# Copy the newly built binary into the operator pod
	kubectl cp ./bin/manager $(REDIS_MONITORING):/manager -n ${NAMESPACE}
	
	# Execute Delve inside the pod so it listens for remote debug connections on port 40001
	kubectl exec -it po/$(REDIS_MONITORING) -n ${NAMESPACE} -- \
		dlv exec /manager \
			--headless \
			--listen=127.0.0.1:40001 \
			--api-version=2 \
			--accept-multiclient \
			--continue

port-forward: ##		Port forwarding of port 40000 for debugging the manager with Delve.
	kubectl port-forward pod/$(REDIS_MONITORING) 40001:40001 -n ${NAMESPACE}


port-forward-metrics: ##		Port forwarding of port 8080 for debugging the manager with Delve.
	kubectl port-forward pod/$(REDIS_MONITORING) 8080:8080 -n ${NAMESPACE}

delete: ##		Delete the operator pod (redis-operator) in order to have a new clean pod created.
	kubectl delete pod $(REDIS_MONITORING) -n ${NAMESPACE}