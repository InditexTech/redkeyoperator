# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

.DEFAULT_GOAL := help
SHELL := /bin/bash

NAME           := redkey-operator
VERSION        := 0.1.0
ROBIN_VERSION  := 0.1.0
GOLANG_VERSION := 1.25.7
DELVE_VERSION  := 1.25

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.0
OPERATOR_SDK_VERSION ?= v1.42.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0

# Openshift platform supported version
OPENSHIFT_VERSION="v4.11"

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

# Auxiliar programs
SED = sed

# Test coverage files
TEST_COVERAGE_PROFILE_OUTPUT = ./local
TEST_COVERAGE_PROFILE_OUTPUT_FILE = coverage.out
TEST_E2E_OUTPUT = .local/results.json
TEST_REPORT_OUTPUT = .local/test_report.ndjson
TEST_REPORT_OUTPUT_E2E = .local/test_report_e2e.ndjson
# .............................................................................
# / END SECTION
# .............................................................................

# .............................................................................
# / IMPORTANT VARIABLES
# .............................................................................
# Allowed deploying profiles. dev: testing in local, debug: testing with delve in the operator and profiling: testing with pprof enabled in the operator for performance analysis
PROFILES := dev debug profiling
# Allowed robin deploying profiles. dev: testing robin in local and debug: testing with delve in robin
PROFILES_ROBIN := dev debug
# Deploying profile used to generate the manifest files to deploy the operator.
# The files to generate the manifests are kustomized from the directory config/deploy-profile/<PROFILE>.
# By default, `dev` profile is used. It can be overwritten (e.g. make process-manifests PROFILE=debug).
# Only the values defined in `PROFILES` are allowed.
PROFILE ?= dev
ifeq ($(filter $(PROFILE),$(PROFILES)), )
$(error The profile specified ($(PROFILE)) is not supported)
endif

PROFILE_ROBIN ?= dev
ifeq ($(filter $(PROFILE_ROBIN),$(PROFILES_ROBIN)), )
$(error The PROFILE_ROBIN specified ($(PROFILE)) is not supported)
endif

CHANNELS ?= alpha,beta,stable

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
DEFAULT_CHANNEL ?= stable
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

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# IMAGE_TAG_BASE defines the docker.io namespace and part of the image name for remote images.
# This variable is used to construct full image tags for bundle and catalog images.
#
# For example, running 'make bundle-build bundle-push catalog-build catalog-push' will build and push both
# inditex.dev/sample-bundle:$VERSION and inditex.dev/sample-catalog:$VERSION.
IMAGE_TAG_BASE ?= localhost:5001/redkey-operator

# Image URL to use for building/pushing image targets
IMG ?= $(IMAGE_TAG_BASE):$(VERSION)
IMG_ROBIN ?= ghcr.io/inditextech/redkey-robin:$(ROBIN_VERSION)

# BUNDLE_IMG defines the image:tag used for the bundle.
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= $(IMAGE_TAG_BASE)-bundle:$(VERSION)

# Image REF in bundle image
# Can be overwritten with make bundle IMAGE_REF=<some-registry>/<project-name-bundle>:<tag>
IMAGE_REF ?= $(IMG)

# Namespace in which the manager will be deployed.
NAMESPACE ?= redkey-operator

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
	$(info $(M) installing dependencies)
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
	$(info $(M) running gofmt checking code style)
	@fmtRes=$$($(GOFMT) -d $(SRC)); \
	if [ -n "$${fmtRes}" ]; then \
		echo "gofmt checking failed!"; echo "$${fmtRes}"; echo; \
		echo "Please ensure you are using $$($(GO) version) for formatting code."; \
		exit 1; \
	fi

.PHONY: fmt
fmt: ## Run gofmt on all source files
	$(info $(M) running go fmt)
	$(GOFMT) -l -w $(SRC)

.PHONY: lint
lint: deps ## Run golint
	$(info $(M) running staticcheck)
	$(GOLINT) ./...

.PHONY: vet
vet: ## Run go vet
	$(info $(M) running go vet)
	$(GO) vet ./...

.PHONY: clean
clean: ## Clean the build artifacts and Go cache
	$(info $(M) cleaning generated files)
	rm -rf ./target
	rm -rf ./bin
	rm -rf ./bundle
	rm -f bundle.Dockerfile
	rm -rf ./deployment
	rm -rf ./certs
	rm -rf .local
	$(GO) clean --modcache

##@ Development
manifests: kustomize controller-gen ##	Generate ClusterRole and CustomResourceDefinition objects.
	$(info $(M) generating config CRD base manifest files from code)
	$(CONTROLLER_GEN) rbac:roleName=redkey-operator-role crd:maxDescLen=0 paths="./..." output:crd:artifacts:config=config/crd/bases
	$(info $(M) setting operator-version annotation in CRD kustomization file)
	cd config/crd && \
	$(KUSTOMIZE) edit set annotation inditex.dev/operator-version:$(VERSION);

generate: controller-gen ##	Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

##@ Build
.PHONY: build
build: ##	Build program binary
	$(info $(M) building Manager executable)
	$(GO) build \
			-ldflags $(GO_COMPILE_FLAGS) \
			-tags release \
			-o bin/manager \
			./cmd/main.go

.PHONY: update-packages
update-packages: ##	Run go get -u
	$(info $(M) running go get -u)
	$(GO) get -u


.PHONY: tidy
tidy: ##	Run go mod tidy
	$(info $(M) running go mod tidy)
	$(GO) mod tidy

.PHONY: run
run: ##	Execute the program locally
	$(info $(M) running app)
	CONFIGMAP_PATH=./config_test/configmap.local.yml SECRET_PATH=./config_test/secrets.local.yml $(GO) run ./cmd/main.go

docker-build: test ##	Build operator docker image from source or an empty image to copy the binary if default profile (uses `${IMG}` image name)
	$(info $(M) building operator docker image)
	if [ ${PROFILE} != "debug" ]; then \
		$(CONTAINER_TOOL) build -t ${IMG} --build-arg GOLANG_VERSION=$(GOLANG_VERSION) . ; \
	else \
		$(CONTAINER_TOOL) build -t ${IMG} -f debug.Dockerfile --build-arg GOLANG_VERSION=$(GOLANG_VERSION) --build-arg DELVE_VERSION=$(DELVE_VERSION) . ; \
	fi
docker-push: ##	Push operator $(CONTAINER_TOOL) image (uses `${IMG}` image name).
	$(info $(M) pushing operator docker image)
	$(CONTAINER_TOOL) push ${IMG}

##@ Deployment
install: create-namespace process-manifests-crd ##		Install CRD into the K8s cluster specified by kubectl default context (Kustomize is installed if not present).
	$(info $(M) applying CRD manifest file)
	kubectl apply -f deployment/redkey.inditex.dev_redkeyclusters.yaml

uninstall: process-manifests-crd ##		Uninstall CRD from the K8s cluster specified by kubectl default context (Kustomize is installed if not present).
	$(info $(M) deleting CRD)
	kubectl delete -f deployment/redkey.inditex.dev_redkeyclusters.yaml

process-manifests: kustomize process-manifests-crd ##		Generate the kustomized yamls into the `deployment` directory to deploy the manager.
	$(info $(M) generating Manager deploying manifest files using ${PROFILE} profile)
	cp config/deploy-profiles/${PROFILE}/kustomization.yaml config/deploy-profiles/${PROFILE}/kustomization.yaml.orig && \
	(cd config/deploy-profiles/${PROFILE} && \
		$(KUSTOMIZE) edit set namespace ${NAMESPACE}) && \
	(cd config/deploy-profiles/${PROFILE} && \
			$(KUSTOMIZE) edit set image redkey-operator=${IMG}); \
		$(KUSTOMIZE) build config/deploy-profiles/${PROFILE} > deployment/manager.yaml; \
		$(SED) -i 's/watch-namespace/$(NAMESPACE)/' deployment/manager.yaml; \
	rm config/deploy-profiles/${PROFILE}/kustomization.yaml
	mv config/deploy-profiles/${PROFILE}/kustomization.yaml.orig config/deploy-profiles/${PROFILE}/kustomization.yaml
	@echo "Manifest generated successfully"

process-manifests-crd: kustomize manifests ##	Generate the kustomized yamls into the `deployment` directory to deploy the CRD.
	$(info $(M) generating CRD deploying manifest files)
	mkdir -p deployment
	$(KUSTOMIZE) build config/crd > deployment/redkey.inditex.dev_redkeyclusters.yaml
	@echo "CRD manifest generated successfully"

deploy: deploy-manager ##		Deploy the manager into the K8s cluster specified by kubectl default context.

undeploy: undeploy-manager ##		Undeploy the manager from the K8s cluster specified by kubectl default context.

redeploy: undeploy-manager deploy-manager ##		Redeploy the manager into the K8s cluster specified by kubectl default context.

deploy-manager: process-manifests ##		Deploy the manager into the K8s cluster specified by kubectl default context.
	$(info $(M) applying Manager manifests)
	kubectl apply -f deployment/manager.yaml

undeploy-manager: process-manifests ##		Delete the manager from the K8s cluster specified by kubectl default context.
	$(info $(M) deleting Manager)
	kubectl delete -f deployment/manager.yaml

OPERATOR=$(shell kubectl -n ${NAMESPACE} get po -l='control-plane=redkey-operator' -o=jsonpath='{.items[0].metadata.name}')
dev-deploy: ##		Build a new manager binary, copy the file to the operator pod and run it.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./cmd/main.go
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l control-plane=redkey-operator
	kubectl cp ./bin/manager $(OPERATOR):/manager -n ${NAMESPACE}
	kubectl exec -it po/$(OPERATOR) -n ${NAMESPACE} exec /manager

debug: ##		Build a new manager binary, copy the file to the operator pod and run it in debug mode (listening on port 40000 for Delve connections).
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags="all=-N -l" -o bin/manager ./cmd/main.go
	kubectl wait -n ${NAMESPACE} --for=condition=ready pod -l control-plane=redkey-operator
	kubectl cp ./bin/manager $(OPERATOR):/manager -n ${NAMESPACE}
	kubectl exec -it po/$(OPERATOR) -n ${NAMESPACE} -- dlv --listen=:40000 --headless=true --api-version=2 --accept-multiclient exec /manager --continue

port-forward: ##		Port forwarding of port 40000 for debugging the manager with Delve.
	kubectl port-forward pods/$(OPERATOR) 40000:40000 -n ${NAMESPACE}

delete-operator: ##		Delete the operator pod (redkey-operator) in order to have a new clean pod created.
	kubectl delete pod $(OPERATOR) -n ${NAMESPACE}

create-namespace: ##		Create namespace if does not exist (uses  ${NAMESPACE})
	$(info Creating namespace if it does not exist $(NAMESPACE))
	@if ! kubectl get namespace $(NAMESPACE) >/dev/null 2>&1; then \
		kubectl create namespace $(NAMESPACE); \
	fi

apply-rkcl:  create-namespace ##		Apply the sample Redkey Cluster manifest.
	$(info $(M) creating sample Redkey cluster)
	$(KUSTOMIZE) build config/examples/ephemeral-robin-$(PROFILE_ROBIN) | $(SED) 's/namespace: redkey-operator/namespace: ${NAMESPACE}/' | $(SED) 's,image: redkey-robin:0.1.0,image: ${IMG_ROBIN},' | kubectl apply -f -

delete-rkcl: ##		Delete the sample Redkey Cluster manifest.
	$(info $(M) deleting sample Redkey cluster)
	$(KUSTOMIZE) build config/examples/ephemeral-robin-$(PROFILE_ROBIN) | $(SED) 's/namespace: redkey-operator/namespace: ${NAMESPACE}/' | $(SED) 's,image: redkey-robin:0.1.0,image: ${IMG_ROBIN},' | kubectl delete -f -

apply-all: docker-build docker-push process-manifests install deploy apply-rkcl

delete-all: delete-rkcl undeploy uninstall

port-forward-profiling: ##		Port forwarding of port 6060 for profiling the manager with pprof.
	kubectl port-forward pods/$(OPERATOR) 6060:6060 -n ${NAMESPACE}

##@ Tools

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN): ## Ensure that the directory exists
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ##		Download kustomize locally if necessary.
$(KUSTOMIZE):
	$(info $(M) ensuring kustomize is available)
	mkdir -p $(LOCALBIN)
	curl -s $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ##		Download controller-gen locally if necessary.
$(CONTROLLER_GEN):
	$(info $(M) ensuring controller-gen is available)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ##		Download envtest-setup locally if necessary.
$(ENVTEST):
	$(info $(M) ensuring envtest-setup is available)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest


##@ Troubleshooting

REDIS_PODS=$(shell kubectl get po -n ${NAMESPACE} --field-selector=status.phase=Running  -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' )
redis-check: ## Check information in pods related to Redkey cluster.
	for POD in $(REDIS_PODS); do kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster check localhost:6379; done

redis-nodes: ## Check nodes of Redkey cluster.
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER NODES | sort; done

redis-slots: ## Check slots of Redkey cluster.
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER SLOTS | sort; done

redis-info: ## Check info of Redkey cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER INFO | sort; done

redis-forget: ## Forget nodes of Redkey cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER FORGET $(nodeid); done

redis-benchmark: # Benckmark a Redkey cluster
	if [ "$(WORKSPACE)" == "default" ]; then \
		kubectl exec -it -n $(NSTEST) redis-cluster-0 -- redis-benchmark -c 100 -n 100000 -t set,get; \
	else \
	  if [ -z "$(REDIS_POD)" ]; then \
	    echo "ERROR:: No pods running to benchmark, starts your cluster to use this feature."; \
	  else \
		kubectl exec -it $(REDIS_POD) -n ${NAMESPACE} -- redis-benchmark -c 200 -n 100000 -d 100 -P 20 -r 10 -t set,get,lpush; \
	  fi \
	fi

redis-fix: ## Fix Redkey cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster fix localhost:6379; done

redis-rebalance: ## Rebalance slots of Redkey cluster
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli --cluster rebalance --cluster-use-empty-masters localhost:6379; done

args = `arg="$(filter-out $@,$(MAKECMDGOALS))" && echo $${arg:-${1}}`

redis-forget-manually:
	for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli CLUSTER FORGET $(call args, $(nodeid)); done

REDIS_POD=$(shell kubectl get po -n ${NAMESPACE} --field-selector=status.phase=Running  -l='redis.redkeycluster.operator/component=redis' -o custom-columns=':metadata.name' --no-headers | head -n 1)

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
	kubectl patch rkcl redis-cluster -n ${NAMESPACE} --type='json' -p='[{"op": "replace", "path": "/spec/config", "value":"maxmemory 200mb\nmaxmemory-samples 5\nmaxmemory-policy allkeys-lru\nappendonly no\nprotected-mode no\nloadmodule /usr/lib/redis/modules/redisgraph.so"}]' | for POD in $(REDIS_PODS); do echo $${POD}; kubectl exec -it $${POD} -n ${NAMESPACE} -- redis-cli SHUTDOWN; done | sleep 5

redis-aof-enable:
	kubectl patch rkcl redis-cluster -n ${NAMESPACE} --type='json' -p='[{"op": "replace", "path": "/spec/config", "value":"maxmemory 200mb\nmaxmemory-samples 5\nmaxmemory-policy allkeys-lru\nappendonly yes\nprotected-mode no\nloadmodule /usr/lib/redis/modules/redisgraph.so"}]'


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
bundle: kustomize manifests operator-sdk ## Generate the files for bundle.
	rm -rf bundle/manifests/*
	rm -rf bundle/metadata/*
	$(OPERATOR_SDK) generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image redkey-operator=$(IMAGE_REF)
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle -q --overwrite --channels $(CHANNELS) --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	$(OPERATOR_SDK) bundle validate ./bundle
	# Remove the images section from the kustomization.yaml added when kustomizing.
	cd config/manager && sed -i '/^images:$$/,/^[^ -]/{ /^images:$$/d; /^- name: redkey-operator$$/,/^  newTag:/d; }' kustomization.yaml && sed -i '/^images:$$/d' kustomization.yaml
	sed -i "/^FROM.*/a LABEL com.redhat.openshift.versions="$(OPENSHIFT_VERSION)"" bundle.Dockerfile; \
	sed -i "/^FROM.*/a LABEL com.redhat.delivery.operator.bundle=true" bundle.Dockerfile; \
	sed -i "/^FROM.*/a LABEL com.redhat.delivery.backport=false" bundle.Dockerfile; \
	sed -i "/^FROM.*/a # Labels for RedHat Openshift Platform" bundle.Dockerfile
	sed -i "/^annotations.*/a \  com.redhat.openshift.versions: "$(OPENSHIFT_VERSION)"" bundle/metadata/annotations.yaml; \
	sed -i "/^annotations.*/a \  # Annotations for RedHat Openshift Platform" bundle/metadata/annotations.yaml

.PHONY: bundle-build
bundle-build: ## Build the bundle image.
	$(CONTAINER_TOOL) build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

bundle-push: ## Push the bundle image.
	$(CONTAINER_TOOL) push $(BUNDLE_IMG)

.PHONY: opm
OPM = $(LOCALBIN)/opm
opm: ## Download opm locally if necessary.
ifeq (,$(wildcard $(OPM)))
ifeq (,$(shell which opm 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPM)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPM) https://github.com/operator-framework/operator-registry/releases/download/v1.55.0/$${OS}-$${ARCH}-opm ;\
	chmod +x $(OPM) ;\
	}
else
OPM = $(shell which opm)
endif
endif

# A comma-separated list of bundle images (e.g. make catalog-build BUNDLE_IMGS=example.com/operator-bundle:v0.1.0,example.com/operator-bundle:v0.2.0).
# These images MUST exist in a registry and be pull-able.
BUNDLE_IMGS ?= $(BUNDLE_IMG)

# The image tag given to the resulting catalog image (e.g. make catalog-build CATALOG_IMG=example.com/operator-catalog:v0.2.0).
CATALOG_IMG ?= $(IMAGE_TAG_BASE)-catalog:v$(VERSION)

# Set CATALOG_BASE_IMG to an existing catalog image tag to add $BUNDLE_IMGS to that image.
ifneq ($(origin CATALOG_BASE_IMG), undefined)
FROM_INDEX_OPT := --from-index $(CATALOG_BASE_IMG)
endif

# Build a catalog image by adding bundle images to an empty catalog using the operator package manager tool, 'opm'.
# This recipe invokes 'opm' in 'semver' bundle add mode. For more information on add modes, see:
# https://github.com/operator-framework/community-operators/blob/7f1438c/docs/packaging-operator.md#updating-your-existing-operator
.PHONY: catalog-build
catalog-build: opm ## Build a catalog image.
	$(OPM) index add --container-tool $(CONTAINER_TOOL) --mode semver --tag $(CATALOG_IMG) --bundles $(BUNDLE_IMGS) $(FROM_INDEX_OPT)

# Push the catalog image.
.PHONY: catalog-push
catalog-push: ## Push a catalog image.
	$(MAKE) docker-push IMG=$(CATALOG_IMG)

##@ Test

TEST_PATHS := ./controllers/ ./internal/*/ ./api/*/

.PHONY: test
test:  ## Execute unit tests
	$(info $(M) running unit tests)
	$(GO) test $(TEST_PATHS)

.PHONY: test-cov
test-cov:  ## Execute the application test with coverage
	$(info $(M) running tests and generating coverage report)
	@mkdir -p $(dir $(TEST_REPORT_OUTPUT))
	$(GO) test $(TEST_PATHS) -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -covermode=count

.PHONY: test-sonar
test-sonar:  ## Execute tests for Sonar (coverage + test report)
	$(info $(M) running tests and generating sonar report)
	@mkdir -p $(dir $(TEST_REPORT_OUTPUT))
	$(GO) test $(TEST_PATHS) -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT) -json > $(TEST_REPORT_OUTPUT)

## Common ginkgo options for e2e tests

.PHONY: ginkgo
ginkgo:
	$(GO) install github.com/onsi/ginkgo/v2/ginkgo


TEST_PARALLEL_PROCESS ?= 4
GOMAXPROCS ?= 4
REDIS_IMAGE ?= redis:8.4.0
CHANGED_REDIS_IMAGE ?= redis:8.2.3

GINKGO_OPTS ?= $(GINKGO_EXTRA_OPTS) -procs=$(TEST_PARALLEL_PROCESS) -vv

GINKGO_ENV ?= GOMAXPROCS=$(GOMAXPROCS) \
	OPERATOR_IMAGE=$(IMG) \
	ROBIN_IMAGE=$(IMG_ROBIN) \
	REDIS_IMAGE=$(REDIS_IMAGE) \
	CHANGED_REDIS_IMAGE=$(CHANGED_REDIS_IMAGE) \
	SIDECARD_IMAGE=$(SIDECARD_IMAGE)

GINKGO_PACKAGES ?= ./test/e2e

.PHONY: test-e2e
test-e2e: process-manifests-crd ginkgo  ## Execute e2e application test
	$(info $(M) running e2e tests...)
	@mkdir -p $(dir $(TEST_E2E_OUTPUT))
	$(GINKGO_ENV) ginkgo --json-report=$(TEST_E2E_OUTPUT) $(GINKGO_OPTS) $(GINKGO_PACKAGES)

.PHONY: test-e2e-cov
test-e2e-cov: process-manifests-crd ginkgo  ## Execute e2e application test with coverage
	$(info $(M) running e2e tests with coverage $(GINKGO_ENV))
	@mkdir -p $(dir $(TEST_COVERAGE_PROFILE_OUTPUT))
	$(GINKGO_ENV) ginkgo \
	     -cover -covermode=count -coverprofile=$(TEST_COVERAGE_PROFILE_OUTPUT_FILE) -output-dir=$(TEST_COVERAGE_PROFILE_OUTPUT) \
		$(GINKGO_OPTS) $(GINKGO_PACKAGES)
