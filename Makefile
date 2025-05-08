# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

.DEFAULT_GOAL := help
SHELL := /bin/bash

name      := redis-cluster-operator
VERSION   := 1.3.0
package   := github.com/inditextech/$(name)
# Image URL to use for building/pushing image targets when using `pro` deployment profile.
IMG ?= redis-operator:$(VERSION)
IMG_WEBHOOK ?= redis-operator-webhook:$(VERSION)

# CN for the webhook certificate
CN ?= inditex.com

# .............................................................................
# DONT TOUCH THIS SECTION
# .............................................................................
# Build specific information
COMMIT?=$(shell git rev-parse HEAD)
DATE?=$(shell date +%FT%T%z)

# Go related variables.
GO = go
GOFMT = gofmt
GOLINT = staticcheck

# go source files, ignore vendor directory
SRC = $(shell find . -path ./vendor -prune -o -name '*.go' -print)
M = $(shell printf "\033[34;1m▶\033[0m")

MODULE=$(shell go list -m)
GO_COMPILE_FLAGS='-X $(MODULE)/cmd/server.GitCommit=$(COMMIT) -X $(MODULE)/cmd/server.BuildDate=$(DATE) -X $(MODULE)/cmd/server.VersionBuild=$(VERSION)'

# Test coverage files
TEST_COVERAGE_PROFILE_OUTPUT = ".local/coverage.out"
TEST_REPORT_OUTPUT = ".local/test_report.ndjson"
TEST_REPORT_OUTPUT_E2E = ".local/test_report_e2e.ndjson"
# .............................................................................
# / END SECTION
# .............................................................................

# .............................................................................
# / IMPORTANT VARIABLES
# .............................................................................
# CHANNELS define the bundle channels used in the bundle. 
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "preview,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=preview,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="preview,fast,stable")
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif

# DEFAULT_CHANNEL defines the default channel used in the bundle. 
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# BUNDLE_IMG defines the image:tag used for the bundle. 
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= controller-bundle:$(VERSION)

# Image URL to use for building/pushing image targets when using `dev` deployment profile.
# We always use version 0.1.0 for this purpose.
IMG_DEV ?= redis-operator:0.1.0-dev
IMG_DEV_WEBHOOK ?= redis-operator-webhook:0.1.0-dev

# Image URL to use for deploying the operator pod when using `debug` deployment profile.
# A base golang image is used with Delve installed, in order to be able to remotely debug the manager.
IMG_DEBUG ?= delve:1.24.0

# Set the Operator SDK version to use. By default, what is installed on the system is used.
# This is useful for CI or a project to utilize a specific version of the operator-sdk toolkit.
OPERATOR_SDK_VERSION ?= v1.37.0

CHANNELS ?= alpha,beta

# Image REF in bundle image
# Can be overwritten with make bundle IMAGE_REF=<some-registry>/<project-name-bundle>:<tag>
IMAGE_REF ?= $(IMG)

# Namespace in which the manager will be deployed.
NAMESPACE ?= redis-operator

# Namespace in which the webhook will be deployed.
WEBHOOK_NAMESPACE := redis-operator-webhook

# Allowed deploying profiles.
PROFILES := dev debug pro

# Deploying profile used to generate the manifest files to deploy the operator.
# The files to generate the manifests are kustomized from the directory config/deploy-profile/<PROFILE>.
# By default, `dev` profile is used. It can be overwritten (e.g. make process-manifests PROFILE=debug).
# Only the values defined in `PROFILES` are allowed.
PROFILE ?= dev
ifeq ($(filter $(PROFILE),$(PROFILES)), )
$(error The profile specified ($(PROFILE)) is not supported)
endif
# .............................................................................
# / END SECTION
# .............................................................................

# .............................................................................
# PUBLIC TARGETS
# .............................................................................

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ##	Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ General
.PHONY: verify
verify: deps tidy checkfmt lint vet build test-cov ## Check the code

deps: ## Installs dependencies
	$(info $(M) installing dependencies...)
	GONOSUMDB=honnef.co/go/* GONOPROXY=honnef.co/go/* $(GO) install honnef.co/go/tools/cmd/staticcheck@v0.6.1

.PHONY: version 
version:: ## Print the current version of the project.
	@echo "$(VERSION)"

.PHONY: version-next
version-next:: ## Bump to next development version
	@echo "Bumping to next development version"
	sed -ri 's/(.*)(VERSION\s*:=\s*)([0-9]+)\.([0-9]+)\.([0-9]+)(.*)/echo "\1\2\3.$$((\4+1)).0-SNAPSHOT\6"/ge' Makefile

.PHONY: version-set
version-set:: ## Set the project version to the given version, using the NEW_VERSION environment variable
	@echo "Setting version to $(NEW_VERSION)"
	sed -ri 's/(.*)(VERSION\s*:=\s*)([0-9]+\.[0-9]+\.[0-9]+)(-SNAPSHOT)(.*)/echo "\1\2$(NEW_VERSION)\5"/ge' Makefile

.PHONY: checkfmt 
checkfmt: ## Check format validation
	$(info $(M) running gofmt checking code style...)
	@fmtRes=$$($(GOFMT) -d $(SRC)); \
	if [ -n "$${fmtRes}" ]; then \
		echo "gofmt checking failed!"; echo "$${fmtRes}"; echo; \
		echo "Please ensure you are using $$($(GO) version) for formatting code."; \
		exit 1; \
	fi

.PHONY: fmt 
fmt: ## Run gofmt on all source files
	$(info $(M) running go fmt...)
	$(GOFMT) -l -w $(SRC)

.PHONY: lint 
lint: deps ## Run golint
	$(info $(M) running staticcheck...)
	$(GOLINT) ./...

.PHONY: vet 
vet: ## Run go vet
	$(info $(M) running go vet...)
	$(GO) vet ./...

.PHONY: clean
clean: ## Clean the build artifacts and Go cache
	$(info $(M) cleaning generated files...)
	rm -rf ./target
	rm -rf ./bin
	rm -rf ./bundle
	rm -f bundle.Dockerfile
	rm -rf ./deployment
	$(GO) clean --modcache

##@ Development
manifests: controller-gen ##	Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=redis-operator-role crd:maxDescLen=0 webhook paths="./..." output:crd:artifacts:config=config/crd/bases

generate: controller-gen ##	Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

##@ Build
.PHONY: build
build: ##	Build program binary
	$(info $(M) building executable...)
	$(GO) build \
			-ldflags $(GO_COMPILE_FLAGS) \
			-tags release \
			-o bin/manager \
			./cmd/main.go

.PHONY: update-packages
update-packages: ##	Run go get -u
	$(info $(M) running go get -u...)
	$(GO) get -u


.PHONY: tidy
tidy: ##	Run go mod tidy
	$(info $(M) running go mod tidy...)
	$(GO) mod tidy

.PHONY: run
run: ##	Execute the program locally
	$(info $(M) running app...)
	CONFIGMAP_PATH=./config_test/configmap.local.yml SECRET_PATH=./config_test/secrets.local.yml $(GO) run ./cmd/main.go

dev-build: ##	Build manager binary.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -o bin/manager ./cmd/main.go

dev-build-webhook: ##	Build webhook binary.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -C webhook -o ../bin/webhook  main.go

docker-build: test ##	Build docker image with the manager (uses `${IMG}` image name).
	docker build -t ${IMG} .

docker-build-webhook: test ##	Build docker image with the manager (uses `${IMG_WEBHOOK}` image name).
	docker build -t ${IMG_WEBHOOK} -f webhook/Dockerfile .

docker-push: ##	Push docker image with the manager (uses `${IMG}` image name).
	docker push ${IMG}

docker-push-webhook: ##	Push docker image with the webhook (uses `${IMG_WEBHOOK}` image name).
	docker push ${IMG_WEBHOOK}

dev-docker-build: test  ##	Build docker image with the manager for development (uses `${IMG_DEV}` image name).
	docker build -t ${IMG_DEV} .

dev-docker-build-webhook: test  ##	Build docker image with the webhook for development (uses `${IMG_DEV_WEBHOOK}` image name).
	docker build -t ${IMG_DEV_WEBHOOK} -f webhook/Dockerfile .

dev-docker-push: ##	Push docker image with the manager for development (uses `${IMG_DEV}` image name).
	docker push ${IMG_DEV}

dev-docker-push-webhook: ##	Push docker image with the webhook for development (uses `${IMG_DEV_WEBHOOK}` image name).
	docker push ${IMG_DEV_WEBHOOK}

debug-docker-build: ##	Build docker image for debugging from debug.Dockerfile (uses `${IMG_DEBUG}` image name).
	docker build -t ${IMG_DEBUG} -f debug.Dockerfile .

debug-docker-push: ##	Push docker image for debugging from debug.Dockerfile (uses `${IMG_DEBUG}` image name).
	docker push ${IMG_DEBUG}


##@ Deployment
install: process-manifests-crd ##		Install CRD into the K8s cluster specified by kubectl default context (Kustomize is installed if not present).
	kubectl apply -f deployment/redis.inditex.com_redisclusters.yaml

uninstall: process-manifests-crd ##		Uninstall CRD from the K8s cluster specified by kubectl default context (Kustomize is installed if not present).
	kubectl delete -f deployment/redis.inditex.com_redisclusters.yaml

process-manifests: kustomize process-manifests-crd process-manifests-webhook ##		Generate the kustomized yamls into the `deployment` directory to deploy the manager.
	@echo "Generating manager deploying manifest files using ${PROFILE} profile"
	cp config/deploy-profiles/${PROFILE}/kustomization.yaml config/deploy-profiles/${PROFILE}/kustomization.yaml.orig && \
	(cd config/deploy-profiles/${PROFILE} && \
		$(KUSTOMIZE) edit set namespace ${NAMESPACE}) && \
	if [ ${PROFILE} == "dev" ]; then \
		(cd config/deploy-profiles/${PROFILE} && \
			$(KUSTOMIZE) edit set image redis-operator=${IMG_DEV}); \
		$(KUSTOMIZE) build config/deploy-profiles/dev > deployment/manager.yaml; \
		$(SED) -i 's/watch-namespace/$(NAMESPACE)/' deployment/manager.yaml; \
	elif [ ${PROFILE} == "debug" ]; then \
		(cd config/deploy-profiles/${PROFILE} && \
			$(KUSTOMIZE) edit set image /redis-operator=${IMG_DEBUG}); \
		$(KUSTOMIZE) build config/deploy-profiles/debug > deployment/manager.yaml; \
		$(SED) -i 's/watch-namespace/$(NAMESPACE)/' deployment/manager.yaml; \
	elif [ ${PROFILE} == "pro" ]; then \
		(cd config/deploy-profiles/${PROFILE} && \
			$(KUSTOMIZE) edit set image redis-operator=${IMG}); \
		$(KUSTOMIZE) build config/deploy-profiles/pro > deployment/manager.yaml; \
	fi
	rm config/deploy-profiles/${PROFILE}/kustomization.yaml
	mv config/deploy-profiles/${PROFILE}/kustomization.yaml.orig config/deploy-profiles/${PROFILE}/kustomization.yaml
	@echo "Manifest generated successfully"

process-manifests-crd: kustomize manifests ##	Generate the kustomized yamls into the `deployment` directory to deploy the CRD.
	@echo "Generating CRD deploying manifest files"
	if [ ! -f certs/ca.crt ]; then make generate-ca-cert; fi
	$(eval CA_CERT := $(shell cat certs/ca.crt | base64 -w 0))
	cp ./config/crd/patches/webhook_in_redisclusters.yaml ./config/crd/patches/webhook_in_redisclusters.yaml.orig
	cat ./config/crd/patches/webhook_in_redisclusters.yaml.tpl | sed "s|WEBHOOK_CA_CERT|${CA_CERT}|g" > ./config/crd/patches/webhook_in_redisclusters.yaml
	$(KUSTOMIZE) build config/crd > deployment/redis.inditex.com_redisclusters.yaml
	mv ./config/crd/patches/webhook_in_redisclusters.yaml.orig ./config/crd/patches/webhook_in_redisclusters.yaml
	@echo "CRD manifest generated successfully"

process-manifests-webhook: kustomize ##	Generate the kustomized yamls into the `deployment` directory to deploy the webhook.
	@echo "Generating webhook deploying manifest files using ${PROFILE} profile"
	if [ ! -f certs/ca.crt ]; then make generate-ca-cert; fi

	@echo "Generating certificates..."
	openssl req -newkey rsa:2048 -nodes -keyout certs/server.key \
  		-subj "/C=AU/CN=${WEBHOOK_NAMESPACE}.${CN}" \
  		-out certs/server.csr
	openssl x509 -req \
		-extfile <(printf "subjectAltName=DNS:redis-operator-webhook-service.${WEBHOOK_NAMESPACE}.svc,DNS:redis-operator-webhook-service.${WEBHOOK_NAMESPACE}.svc.cluster.local") \
		-in certs/server.csr \
		-CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial \
		-out certs/server.crt \
		-days 365

	@echo ">> Generating kube secrets..."
	kubectl create secret tls redis-operator-webhook-service-cert \
		--cert=certs/server.crt \
		--key=certs/server.key \
		--dry-run=client -o yaml \
		> ./config/webhook/bases/secret.yaml

	cp config/webhook/kustomization.yaml config/webhook/kustomization.yaml.orig && \
	(cd config/webhook && \
		$(KUSTOMIZE) edit set namespace ${WEBHOOK_NAMESPACE}) && \
	if [ ${PROFILE} == "dev" ]; then \
		(cd config/webhook && \
			$(KUSTOMIZE) edit set image redis-operator-webhook=${IMG_DEV_WEBHOOK};) \
	elif [ ${PROFILE} == "pro" ]; then \
		(cd config/webhook && \
			$(KUSTOMIZE) edit set image redis-operator-webhook=${IMG_WEBHOOK}); \
		$(KUSTOMIZE) build config/deploy-profiles/pro > deployment/manager.yaml; \
	fi
	$(KUSTOMIZE) build config/webhook > deployment/webhook.yaml
	rm config/webhook/kustomization.yaml
	mv config/webhook/kustomization.yaml.orig config/webhook/kustomization.yaml
	rm config/webhook/bases/secret.yaml

	@echo "Webhook manifest generated successfully"

generate-ca-cert:
	@echo "Generating CA certificates"
	mkdir -p certs
	openssl genrsa -out certs/ca.key 2048
	openssl req -x509 -new -nodes -key certs/ca.key -days 3650 -out certs/ca.crt -subj "/CN=${WEBHOOK_NAMESPACE}.${CN}"

deploy: process-manifests ##		Deploy the webhook and the manager into the K8s cluster specified by kubectl default context.
	kubectl apply -f deployment/webhook.yaml
	kubectl apply -f deployment/manager.yaml

undeploy: process-manifests ##		Undeploy the webhook and the operator from the K8s cluster specified by kubectl default context.
	kubectl delete -f deployment/webhook.yaml
	kubectl delete -f deployment/manager.yaml

deploy-manager: process-manifests-webhook ##		Deploy the manager into the K8s cluster specified by kubectl default context.
	kubectl apply -f deployment/manager.yaml

undeploy-manager: process-manifests-webhook ##		Delete the manager from the K8s cluster specified by kubectl default context.

deploy-webhook: process-manifests-webhook ##		Deploy the webhook into the K8s cluster specified by kubectl default context.
	kubectl apply -f deployment/webhook.yaml

undeploy-webhook: process-manifests-webhook ##		Delete the webhook from the K8s cluster specified by kubectl default context.
	kubectl delete -f deployment/webhook.yaml

OPERATOR=$(shell kubectl -n ${NAMESPACE} get po -l='control-plane=redis-operator' -o=jsonpath='{.items[0].metadata.name}')
dev-deploy: ## 		Build a new manager binary, copy the file to the operator pod and run it.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./cmd/main.go
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l control-plane=redis-operator
	kubectl cp ./bin/manager $(OPERATOR):/manager -n ${NAMESPACE}
	kubectl exec -it po/$(OPERATOR) -n ${NAMESPACE} exec /manager

debug: ##		Build a new manager binary, copy the file to the operator pod and run it in debug mode (listening on port 40000 for Delve connections).
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./cmd/main.go
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l control-plane=redis-operator
	kubectl cp ./bin/manager $(OPERATOR):/manager -n ${NAMESPACE}
	kubectl exec -it po/$(OPERATOR) -n ${NAMESPACE} -- dlv --listen=:40000 --headless=true --api-version=2 --accept-multiclient exec /manager --continue

port-forward: ##		Port forwarding of port 40000 for debugging the manager with Delve.
	kubectl port-forward pods/$(OPERATOR) 40000:40000 -n ${NAMESPACE}

delete-operator: ##		Delete the operator pod (redis-operator) in order to have a new clean pod created.
	kubectl delete pod $(OPERATOR) -n ${NAMESPACE}

dev-apply-rdcl: ##		Apply the sample Redis Cluster manifest.
	$(KUSTOMIZE) build config/samples | $(SED) 's/namespace: redis-operator/namespace: ${NAMESPACE}/' | kubectl apply -f -

dev-delete-rdcl: ##		Delete the sample Redis Cluster manifest.
	$(KUSTOMIZE) build config/samples | $(SED) 's/namespace: redis-operator/namespace: ${NAMESPACE}/' | kubectl delete -f -

dev-apply-all: dev-docker-build dev-docker-push install process-manifests deploy dev-apply-rdcl

deploy-aks: install deploy-aks-manifests dev-apply-rdcl

##@ Tools

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN): ## Ensure that the directory exists
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
KUSTOMIZE_VERSION ?= v5.3.0
CONTROLLER_TOOLS_VERSION ?= v0.17.2

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ##		Download kustomize locally if necessary.
$(KUSTOMIZE):
	mkdir -p $(LOCALBIN)
	curl -s $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ##		Download controller-gen locally if necessary.
$(CONTROLLER_GEN):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ##		Download envtest-setup locally if necessary.
$(ENVTEST):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest


##@ Troubleshooting

REDIS_PODS=$(shell kubectl get po -n ${NAMESPACE} --field-selector=status.phase=Running  -l='redis.rediscluster.operator/component=redis' -o custom-columns=':metadata.name' )
redis-check: ## Check information in pods related to redis cluster.
	for POD in $(REDIS_PODS); do kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster check localhost:6379; done

redis-nodes: ## Check nodes of redis cluster.
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER NODES | sort; done

redis-slots: ## Check slots of redis cluster.
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER SLOTS | sort; done

redis-info: ## Check info of redis cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER INFO | sort; done

redis-forget: ## Forget nodes of redis cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER FORGET $(nodeid); done

redis-benchmark: # Benckmark a redis cluster
	if [ "$(WORKSPACE)" == "default" ]; then \
  		kubectl exec -it -n $(NSTEST) redis-cluster-0 -- redis-benchmark -c 100 -n 100000 -t set,get; \
	else \
	  if [ -z "$(REDIS_POD)" ]; then \
	    echo "ERROR:: No pods running to benchmark, starts your cluster to use this feature."; \
	  else \
		kubectl exec -it $(REDIS_POD) -n ${NAMESPACE} -- redis-benchmark -c 200 -n 100000 -d 100 -P 20 -r 10 -t set,get,lpush; \
	  fi \
	fi

redis-fix: ## Fix redis cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster fix localhost:6379; done

redis-rebalance: ## Rebalance slots of redis cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster rebalance --cluster-use-empty-masters localhost:6379; done

args = `arg="$(filter-out $@,$(MAKECMDGOALS))" && echo $${arg:-${1}}`

redis-forget-manually:
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER FORGET $(call args, $(nodeid)); done

REDIS_POD=$(shell kubectl get po -n ${NAMESPACE} --field-selector=status.phase=Running  -l='redis.rediscluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head -n 1)

.SILENT:
redis-insert:
	rm -f /tmp/data.txt
	for ((i=1;i<=100;i++)); do echo "set $${RANDOM}$${RANDOM} $${RANDOM}$${RANDOM}" >> /tmp/data.txt; done
	if [ "$(WORKSPACE)" == "test" ]; then \
  		cat /tmp/data.txt | kubectl exec -i -n $(NSTEST) redis-cluster-0 -- redis-cli -c; \
	else \
	  if [ -z "$(REDIS_POD)" ]; then \
	    echo "ERROR:: No pods running to insert data, starts your cluster to use this feature."; \
	  else \
    	cat /tmp/data.txt | kubectl exec -i $(REDIS_POD) -n ${NAMESPACE} -- redis-cli -c; \
	  fi \
	fi


redis-save:
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli SAVE; kubectl exec $${POD} -n ${NAMESPACE} -- cat /data/dump.rdb > /tmp/$${POD}-dump.rdb; kubectl exec $${POD} -n ${NAMESPACE} -- rm /data/dump.rdb; done

redis-flushall:
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli FLUSHALL; done

redis-restore:
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl cp /tmp/$${POD}-dump.rdb $${POD}:/data/dump.rdb -n ${NAMESPACE}; done

redis-aof-disable:
	kubectl patch rdcl redis-cluster -n ${NAMESPACE} --type='json' -p='[{"op": "replace", "path": "/spec/config", "value":"maxmemory 200mb\nmaxmemory-samples 5\nmaxmemory-policy allkeys-lru\nappendonly no\nprotected-mode no\nloadmodule /usr/lib/redis/modules/redisgraph.so"}]' | for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli SHUTDOWN; done | sleep 5

redis-aof-enable:
	kubectl patch rdcl redis-cluster -n ${NAMESPACE} --type='json' -p='[{"op": "replace", "path": "/spec/config", "value":"maxmemory 200mb\nmaxmemory-samples 5\nmaxmemory-policy allkeys-lru\nappendonly yes\nprotected-mode no\nloadmodule /usr/lib/redis/modules/redisgraph.so"}]'


##@ Integration with OLM

.PHONY: operator-sdk
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
operator-sdk: ## Download operator-sdk locally if necessary.
ifeq (,$(wildcard $(OPERATOR_SDK)))
ifeq (, $(shell which operator-sdk 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH} ;\
	chmod +x $(OPERATOR_SDK) ;\
	}
else
OPERATOR_SDK = $(shell which operator-sdk)
endif
endif

.PHONY: bundle 
bundle: kustomize manifests ## Generate the files for bundle.
	rm -rf bundle/manifests/*
	rm -rf bundle/metadata/*
	$(OPERATOR_SDK) generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image redis-operator=$(IMAGE_REF)
	$(KUSTOMIZE) build config | $(OPERATOR_SDK) generate bundle -q --overwrite --channels $(CHANNELS) --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	$(OPERATOR_SDK) bundle validate ./bundle

.PHONY: bundle-build 
bundle-build: ## Build the bundle image.
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

bundle-push: ## Push the bundle image.
	docker push $(BUNDLE_IMG)


##@ Test
ginkgo:
	go install github.com/onsi/ginkgo/v2/ginkgo

.PHONY: test-e2e
test-e2e: process-manifests-crd ginkgo ## Execute e2e application test
	$(info $(M) running e2e tests...)
	$(info $(M) generating sonar report...)
	$(eval TEST_COVERAGE_PROFILE_OUTPUT_DIRNAME=$(shell dirname $(TEST_COVERAGE_PROFILE_OUTPUT)))
	$(eval TEST_REPORT_OUTPUT_DIRNAME=$(shell dirname $(TEST_REPORT_OUTPUT_E2E)))
	mkdir -p $(TEST_COVERAGE_PROFILE_OUTPUT_DIRNAME) $(TEST_REPORT_OUTPUT_DIRNAME)
	ginkgo ./test/e2e -cover -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -json > $(TEST_REPORT_OUTPUT_E2E)

.PHONY: test-e2e-cov
test-e2e-cov: process-manifests-crd ginkgo ## Execute e2e application test
	$(info $(M) generating coverage report...)
	$(eval TEST_REPORT_OUTPUT_DIRNAME=$(shell dirname $(TEST_REPORT_OUTPUT)))
	mkdir -p $(TEST_REPORT_OUTPUT_DIRNAME)
	ginkgo ./test/e2e -vv -cover -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -covermode=count OPERATOR_IMAGE=$(IMG_DEV)


.PHONY: test-sonar 
test-sonar: ## Execute the application test for Sonar (coverage + test report)
	$(info $(M) running tests and generating sonar report...)
	$(eval TEST_COVERAGE_PROFILE_OUTPUT_DIRNAME=$(shell dirname $(TEST_COVERAGE_PROFILE_OUTPUT)))
	$(eval TEST_REPORT_OUTPUT_DIRNAME=$(shell dirname $(TEST_REPORT_OUTPUT)))
	mkdir -p $(TEST_COVERAGE_PROFILE_OUTPUT_DIRNAME) $(TEST_REPORT_OUTPUT_DIRNAME)
	$(GO) test ./controllers/ ./internal/*/ -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -json > $(TEST_REPORT_OUTPUT)

.PHONY: test-cov
test-cov: ## Execute the application test with coverage
	$(info $(M) running tests and generating coverage report...)
	$(eval TEST_REPORT_OUTPUT_DIRNAME=$(shell dirname $(TEST_REPORT_OUTPUT)))
	mkdir -p $(TEST_REPORT_OUTPUT_DIRNAME)
	$(GO) test ./controllers/ ./internal/*/ ./api/*/ -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -covermode=count

# Integration tests
SED = $(shell which sed)

int-test-generate: ## Generate manifests and metadata files.
	cd config/int-test; $(KUSTOMIZE) edit set image redis-operator=${IMG}; \
	$(KUSTOMIZE) edit set namespace $(NAMESPACE); $(KUSTOMIZE) build > tests.yaml

int-test-clean: ## Clear manifests and metadata files.
	-kubectl delete -f config/int-test/tests.yaml
	-rm config/int-test/tests.yaml

int-test-apply: ## Apply manifests and metadata files.
	$(KUSTOMIZE) build config/crd/ | kubectl apply -f -
	sleep 3 # We need to sleep for a bit to make sure the CRDs are registered before we apply the test
	kubectl apply -f config/int-test/tests.yaml

# .PHONY=int-test 
# int-test: kustomize int-test-generate int-test-apply ## Generate manifests and metadata, then apply generated files.
