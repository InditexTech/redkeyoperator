<!--
SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL S.A. (INDITEX S.A.)

SPDX-License-Identifier: CC-BY-SA-4.0
-->

# AGENTS.md

Reference guide for AI agents and automated tools working on the Redkey Operator codebase.

---

## Project Summary

**Redkey Operator** is a Kubernetes operator that deploys and manages Redkey clusters — key/value clusters built from [Redis](https://hub.docker.com/_/redis) or [Valkey](https://hub.docker.com/r/valkey/valkey/) official images.

It implements the [operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) and extends the Kubernetes API with two custom resources:

- **`RedkeyCluster`** — declares the desired state of a Redis/Valkey cluster (topology, storage, scaling, authentication, etc.).
- **`RedkeyClusterConfig`** — tracks individual configuration revisions applied to a cluster.

The operator reconciles the declared state, manages the lifecycle of Kubernetes objects (StatefulSets, Services, PodDisruptionBudgets, PVCs), and coordinates Redis-side operations through **Redkey Robin**.

### Technology Stack

| Layer | Technology |
| ----- | ---------- |
| Language | Go 1.26+ |
| Framework | [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder) + [operator-sdk](https://github.com/operator-framework/operator-sdk) |
| Kubernetes client | [controller-runtime](https://sigs.k8s.io/controller-runtime) v0.21 |
| Target platform | Kubernetes v1.33 (also OpenShift v4.11+) |
| Packaging | Helm charts (`charts/redkey-operator`, `charts/redkey-cluster`) + OLM bundle |
| Testing | Go `testing`, [Ginkgo v2](https://github.com/onsi/ginkgo) + [Gomega](https://github.com/onsi/gomega), [envtest](https://sigs.k8s.io/controller-runtime/tools/setup-envtest) |
| Linting | [golangci-lint](https://github.com/golangci/golangci-lint) v2.1.0 |

---

## Build Commands

All common operations are driven by `make`. Tools that are not yet present are downloaded automatically into `bin/`.

### Prerequisites (installed manually)

- Go 1.26+
- Make
- Docker or Podman
- `kubectl`

### Code generation (run after changing API types)

```shell
make generate    # regenerates DeepCopy methods
make manifests   # regenerates CRDs, RBAC manifests, and webhooks
```

### Format and static analysis

```shell
make fmt         # go fmt ./...
make vet         # go vet ./...
make lint        # golangci-lint run
make lint-fix    # golangci-lint run --fix
```

### Binary build

```shell
make build       # runs manifests, generate, fmt, vet, test-all, then: go build -o bin/manager cmd/main.go
```

### Container image

```shell
make docker-build              # builds image tagged as localhost:5005/redkey-operator:<VERSION>
make docker-push               # pushes the image
make docker-buildx             # cross-platform build (linux/amd64 + linux/arm64) and push
```

Override the image tag with `IMG=<registry>/<name>:<tag>`.

### Installer manifest

```shell
make build-installer           # produces dist/install.yaml (CRDs + Deployment, via kustomize)
```

### Full CI verification

```shell
make verify      # fmt + vet + lint + build + test-all
```

---

## Testing Instructions

### Mandatory validation for every change

For every change in this repository, before considering the task complete, run:

```shell
make lint
make test-all
```

This requirement is mandatory even if the change is small.

### Unit tests

```shell
make test
```

Runs all packages except `e2e` and `test/integration`. Generates a coverage profile at `cover.out`.

```shell
make coverage    # opens HTML coverage report from cover.out
```

### Integration tests (envtest)

Requires the envtest binaries to be present. The target installs them automatically.

```shell
make test-integration
```

Uses `KUBEBUILDER_ASSETS` to point the test suite to fake Kubernetes API binaries; no real cluster required.

### All tests (unit + integration)

```shell
make test-all
```

### End-to-end tests

Requires a Kind cluster and a running local registry.

```shell
make setup-test-e2e   # creates Kind cluster + loads manager image
make test-e2e         # runs ./test/e2e/ with Ginkgo
make cleanup-test-e2e # tears down the e2e Kind cluster
```

Override the cluster name with `KIND_CLUSTER_E2E=<name>`.

### Local development cluster

```shell
make setup-kind       # creates Kind cluster + local registry on port 5005
make install          # installs CRDs into the current kubeconfig cluster
make run              # runs the controller locally against the cluster
make deploy-samples   # deploys the sample RedkeyCluster resource
make cleanup-kind     # tears down the Kind cluster
```

---

## Style Guidelines

### Code conventions

- Follow standard Go idioms and the [Effective Go](https://go.dev/doc/effective_go) guidelines.
- For every change, run `make lint` and `make test-all` before finishing the task.
- All code must pass `go fmt`, `go vet`, and `golangci-lint` before being merged.
- API types live in `api/v1beta1/`. After changing them, always run `make generate && make manifests`.
- Controller logic lives in `internal/controller/`. Files are split by concern:
  - `redkeycluster_controller.go` — main reconcile loop
  - `redkeycluster_config.go` — `RedkeyClusterConfig` lifecycle
  - `redkeycluster_robin.go` — Redkey Robin integration
- Tests mirror their subject file with a `_test.go` suffix in the same package.
- Use `go.uber.org/zap` through the controller-runtime logger; do not use `fmt.Print*` for operational output.

### Architecture conventions

- The operator is stateless: all state is persisted in Kubernetes resources.
- Reconcile functions must be idempotent.
- `RedkeyClusterConfig` objects carry the annotation `redkey.inditex.dev/cluster-generation` to correlate config revisions with the parent cluster generation.
- Cleanup logic retains the last `ConfigPhaseApplied` config and any newer configs; do not assume older configs survive cleanup.
- REUSE compliance is required: every source file must have an `SPDX-FileCopyrightText` and `SPDX-License-Identifier` header. See `REUSE.toml` and `hack/boilerplate.go.txt`.

### Dependency management

- Use `go mod tidy` after adding or removing dependencies.
- Do not vendor dependencies; the project relies on the Go module cache.

---

## Commit and PR Management

### Commit format

Follow the [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) specification:

```text
<type>(<optional scope>): <short description>

[optional body]

[optional footers]
Signed-off-by: Name <email>
```

Common types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`.

### Required commit properties

Every commit in a PR **must**:

1. Include a `Signed-off-by` trailer (`git commit -s`). This certifies agreement with the [CLA](./CLA.md).
2. Be GPG-signed with a verified key (`git commit -S`, or set `git config --local commit.gpgsign true`).
3. Use a verified email address associated with the GitHub account.

### Pull Request guidelines

- Open an issue before starting significant work and reference it in the PR (`Closes #<issue>`).
- Check existing issues and PRs to avoid duplicate work.
- Keep PRs focused; split unrelated changes into separate PRs.
- For every change, ensure `make lint` and `make test-all` pass locally before opening or updating a PR.
- Ensure `make verify` passes locally before opening a PR.
- Document new features or behaviour changes in `docs/`.
- Add or update tests for every code change.
- An automated check will validate commit signatures and CLA compliance on every PR.
