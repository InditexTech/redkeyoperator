# Redkey Architecture Refactoring Proposal

## Table of Contents

<img src="images/redkey-logo.png" alt="Redkey Logo" width="200" align="right" />

1. [Overview](#1-overview)
2. [Current Architecture](#2-current-architecture)
   - 2.1 [Redkey Operator](#21-redkey-operator)
   - 2.2 [Redkey Robin](#22-redkey-robin)
   - 2.3 [Current Communication Flow](#23-current-communication-flow)
3. [Proposed Architecture](#3-proposed-architecture)
   - 3.1 [New Intermediate CRD: `RedkeyClusterConfig`](#31-new-intermediate-crd-redkeyclusterconfig)
   - 3.2 [Sequenced Configuration Changes](#32-sequenced-configuration-changes)
   - 3.3 [Redkey Operator (Umbrella Coordinator)](#33-redkey-operator-umbrella-coordinator)
   - 3.4 [Redkey Robin (Full Business Logic Owner)](#34-redkey-robin-full-business-logic-owner)
   - 3.5 [Proposed Communication Flow](#35-proposed-communication-flow)
4. [Changes by Repository](#4-changes-by-repository)
   - 4.1 [Redkey Operator Changes](#41-redkey-operator-changes)
   - 4.2 [Redkey Robin Changes](#42-redkey-robin-changes)
5. [Justification](#5-justification)
6. [Impact Assessment](#6-impact-assessment)

---

## 1. Overview

This document proposes a significant architectural refactoring of the Redkey system, which manages Redis/Valkey clusters on Kubernetes. The goal is to invert the current distribution of responsibilities between the two components — **Redkey Operator** and **Redkey Robin** — moving all cluster and Kubernetes resource management logic into Redkey Robin, and reducing the Operator to a lightweight umbrella coordinator.

The REST API currently exposed by Redkey Robin is eliminated, and inter-component communication is replaced by a Kubernetes-native, declarative model based on a new Custom Resource Definition (CRD).

---

## 2. Current Architecture

### 2.1 Redkey Operator

Redkey Operator implements the Kubernetes Operator pattern watching `RedkeyCluster` custom resources and managing their full lifecycle. Its current responsibilities include:

**Kubernetes resource management** — for each `RedkeyCluster` the Operator creates and owns:

- A `ConfigMap` (`<cluster-name>`) containing the Redis configuration (`redis.conf`), built by merging user-provided settings with defaults, calculated memory limits, and authentication secrets.
- A `StatefulSet` (`<cluster-name>`) running the Redis/Valkey pods, with `replicas = primaries + primaries × replicasPerPrimary`.
- A headless `Service` (`<cluster-name>`) for stable DNS within the cluster.
- A `Deployment` (`<cluster-name>-robin`) for the Robin pod.
- A `ConfigMap` (`<cluster-name>-robin`) carrying Robin's full YAML configuration, including cluster topology, reconciliation intervals, and metric settings.
- Optionally, a `PodDisruptionBudget` (`<cluster-name>`) for high availability.

**State machine orchestration** — the Operator drives a cluster through the following status lifecycle, with each status handled by a dedicated reconciliation function:

```ascii
New → Initializing → Configuring → Ready
                                    ↓
                       Upgrading / ScalingUp / ScalingDown
                                ↓       ↓
                              Ready   Error
```

**REST API consumption** — to perform cluster-level Redis operations, the Operator calls Robin's HTTP API at each relevant reconciliation step:

| Operation | Endpoint |
| --------- | -------- |
| Read cluster status | `GET /v1/redkeycluster/status` |
| Read current topology | `GET /v1/redkeycluster/replicas` |
| Apply topology change | `PUT /v1/redkeycluster/replicas` |
| Move slots | `PUT /v1/cluster/move` |
| Check cluster integrity | `GET /v1/cluster/check` |
| Fix cluster issues | `PUT /v1/cluster/fix` |
| Recreate cluster | `PUT /v1/cluster/recreate` |
| Reset individual node | `PUT /v1/cluster/reset/{nodeIndex}` |
| List all nodes | `GET /v1/cluster/nodes` |

### 2.2 Redkey Robin

Redkey Robin is deployed 1:1 per `RedkeyCluster`. Its current responsibilities are:

- **REST API server** — exposes the endpoints listed above, translating HTTP requests into direct Redis operations.
- **Cluster reconciler** — runs a continuous background loop (configurable interval) driving a cluster-level state machine: `Configuring → Ready → ScalingUp / ScalingDown / Upgrading / Resharding / Rebalancing / Fixing`.
- **Redis/Valkey management** — directly manages the cluster topology at the Redis level: running `CLUSTER MEET`, `CLUSTER REPLICATE`, `CLUSTER FORGET`, slot assignment, resharding, rebalancing, failovers, version upgrades.
- **Metrics poller** — collects Redis `INFO` metrics from all nodes at a configurable interval and exposes them as Prometheus metrics at `/metrics`.

Robin's startup configuration is read from the `<cluster-name>-robin` ConfigMap created by the Operator, which encodes the desired cluster topology (`primaries`, `replicasPerPrimary`), the cluster's K8s coordinates, and all timing parameters.

### 2.3 Current Communication Flow

```ascii
┌────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                  │
│                                                        │
│  User                                                  │
│   │  apply RedkeyCluster CR                            │
│   ▼                                                    │
│  ┌──────────────────────────┐                          │
│  │      Redkey Operator     │                          │
│  │  - Watches RedkeyCluster │                          │
│  │  - Creates all K8s objs  │──────────────────────┐   │
│  │  - Drives state machine  │  HTTP REST API calls │   │
│  │  - Updates CR status     │◄────────────────────┐│   │
│  └──────────────────────────┘                     ││   │
│           │ creates / manages                     ││   │
│           ▼                                       ││   │
│  ┌──────────────────────────────────────────────┐ ││   │
│  │  StatefulSet │ Service │ PDB │ ConfigMaps    │ ││   │
│  └──────────────────────────────────────────────┘ ││   │
│           │                                       ││   │
│           ▼                                       ││   │
│  ┌──────────────────────────┐                     ││   │
│  │      Redkey Robin        │◄────────────────────┘│   │
│  │  - REST API (consumed    │──────────────────────┘   │
│  │    by Operator)          │                          │
│  │  - Reconciler loop       │                          │
│  │  - Metrics poller        │                          │
│  └──────────────────────────┘                          │
│           │ manages                                    │
│           ▼                                            │
│  ┌──────────────────────────┐                          │
│  │    Redis/Valkey Cluster  │                          │
│  └──────────────────────────┘                          │
└────────────────────────────────────────────────────────┘
```

---

## 3. Proposed Architecture

### 3.1 New Intermediate CRD: `RedkeyClusterConfig`

The communication channel between the Operator and Robin will be a new CRD, `RedkeyClusterConfig`, preferred over a plain ConfigMap for the following reasons:

- It supports a dedicated **status subresource**, enabling Robin to write status back without conflicting with spec updates written by the Operator.
- It integrates naturally with `kubectl get`/`describe` and Kubernetes events, making the system observable without additional tooling.
- It can carry generation and resource version metadata, enabling Robin to detect whether a spec change has already been reconciled.
- It enables RBAC permissions to be scoped precisely — the Operator gets write access on spec; Robin gets write access on status.

Proposed structure:

```yaml
apiVersion: redkey.inditex.com/v1
kind: RedkeyClusterConfig
metadata:
  name: <cluster-name>-<sequence>   # e.g. mycluster-3
  namespace: <namespace>
  labels:
    redkey.inditex.com/cluster: <cluster-name>   # enables listing all configs for a cluster
spec:
  # Sequence number — monotonically increasing, set by the Operator
  sequence: <int>
  # Skip-if-superseded policy (see Section 3.2)
  skipIfSuperseded: <bool>   # default: false
  # Desired topology
  primaries: <int>
  replicasPerPrimary: <int>
  # Storage configuration
  ephemeral: <bool>
  storage: <string>
  storageClassName: <string>
  # Redis image and version
  image: <string>
  version: <string>
  # Redis runtime configuration (merged redis.conf content)
  redisConfig: <string>
  # Resource requests/limits for Redis pods
  resources: <ResourceRequirements>
  # Auth reference
  auth:
    secretName: <string>
  # Additional pod labels
  labels: <map>
  # PVC deletion policy
  deletePVC: <bool>
  # Fast upgrade strategy (ephemeral only)
  purgeKeysOnRebalance: <bool>
  # Pod template overrides
  override: <RedkeyClusterOverrideSpec>
  # PDB configuration
  pdb: <Pdb>
  # Robin's own configuration overrides (intervals, etc.)
  robinConfig: <RobinConfig>
status:
  # Configuration lifecycle phase (see Section 3.2)
  configPhase: <string>   # Pending | InProgress | Applied
  # Cluster operational status (reported by Robin only when this config is active)
  phase: <string>           # Initializing | Configuring | Ready | ScalingUp | ScalingDown | Upgrading | Rebalancing | Error | Maintenance
  substatus:
    status: <string>
    upgradingPartition: <int>
  nodes:
    <node-name>:
      role: <string>        # primary | replica
      ip: <string>
      replicationStatus: <string>
  conditions:
    - type: Ready
      status: "True"/"False"
    - type: Upgrading
      status: "True"/"False"
    - type: ScalingUp / ScalingDown
      status: "True"/"False"
  lastUpdatedAt: <timestamp>
  observedGeneration: <int>
```

The `spec` section is written exclusively by Redkey Operator. The `status` section is written exclusively by Redkey Robin (via the status subresource). This clean ownership boundary eliminates race conditions and removes the need for any HTTP communication.

This separation must be enforced at the RBAC level and represents a deliberate design constraint. Existing RBAC rules for both components must be reviewed and tightened accordingly:

- **Redkey Operator** is granted `get/list/watch/create/delete` on `RedkeyClusterConfig` (the main resource), which covers creation of new configs and deletion of superseded ones. It is explicitly **not** granted `update/patch` on `RedkeyClusterConfig/status`, so it cannot overwrite Robin's reported state.
- **Redkey Robin** is granted `get/list/watch` on `RedkeyClusterConfig` (to read the desired spec) and `update/patch` on `RedkeyClusterConfig/status` only (the status subresource). It is explicitly **not** granted write access to the main resource, so it cannot alter the desired spec set by the Operator.

Kubernetes enforces this split natively: when the `status` subresource is enabled on a CRD, writes to `/status` and writes to the main object are independent API calls controlled by separate RBAC rules. Neither component can accidentally cross the boundary.

### 3.2 Sequenced Configuration Changes

Rather than updating a single `RedkeyClusterConfig` in-place on every `RedkeyCluster` change, the Operator creates a **new** `RedkeyClusterConfig` CR for each configuration change. This produces an ordered sequence of desired states that Robin processes one at a time.

#### Naming and ordering

The Operator maintains a monotonically increasing **sequence counter**, persisted as an annotation on the `RedkeyCluster` CR itself (`redkey.inditex.com/config-sequence`). Each new `RedkeyClusterConfig` is named `<cluster-name>-<sequence>` and carries the counter in `spec.sequence`. All configs for the same cluster share the label `redkey.inditex.com/cluster: <cluster-name>`, enabling efficient listing.

#### Configuration lifecycle

Each `RedkeyClusterConfig` goes through the following lifecycle in `status.configPhase`:

```ascii
Pending  ──►  InProgress  ──►  Applied  ──►  (deleted by Operator)
```

- **`Pending`** — Created by the Operator. Robin has not yet started processing it.
- **`InProgress`** — Robin is actively applying this configuration (creating/updating K8s resources, performing Redis operations).
- **`Applied`** — The configuration has been fully applied. This CR is now the current baseline representing the actual cluster state.

Once a config reaches `Applied`, it becomes the **current** config. The previously `Applied` config (the baseline from which this change was made) is now superseded and will be deleted by the Operator during its next periodic cleanup pass.

#### Processing order

By default, Robin processes configs **strictly in sequence order** (ascending `spec.sequence`). This guarantees that every intermediate state is visited, which is important for operations where ordering matters (e.g., a scale-up must complete before a subsequent version upgrade can begin safely).

However, in some scenarios it is desirable to **skip intermediate `Pending` configs** and jump directly to the latest one. For example, if two consecutive scaling changes are queued (`primaries: 3 → 5 → 7`), only the final target (`7`) matters — the intermediate state (`5`) can be bypassed. To support this, the `RedkeyClusterConfig` spec includes an optional field:

```yaml
spec:
  skipIfSuperseded: <bool>   # default: false
```

When `skipIfSuperseded` is `true` and Robin detects that a newer config with a higher sequence exists that is also `Pending`, Robin may skip the current config by transitioning it directly from `Pending` to `Applied` (marking it as superseded without executing any changes) and proceed to the next one. When `false` (the default), Robin always processes the config regardless of subsequent pending configs.

The Operator can set `skipIfSuperseded` based on heuristics (e.g., if only `primaries` or `replicasPerPrimary` changed between two consecutive configs) or expose it as a user-configurable policy in the `RedkeyCluster` spec.

#### Cleanup

The **Operator** is responsible for cleaning up superseded configs (Robin only writes status, never deletes CRs). During its periodic reconciliation loop, the Operator:

1. Lists all `RedkeyClusterConfig` CRs for each cluster (using the label selector).
2. Identifies all CRs with `configPhase: Applied`.
3. Keeps only the one with the highest `spec.sequence` (the current baseline).
4. Deletes all older `Applied` CRs.

This ensures that at any point in time the namespace contains at most: one `Applied` CR (current state) + zero or more `Pending`/`InProgress` CRs (queued changes).

#### Example: rapid consecutive changes

```ascii
Time   Operator action                Robin action              State in namespace
─────  ─────────────────────────────  ────────────────────────  ──────────────────────────────
t=0    Creates mycluster-1 (Pending)                            mycluster-1 (Pending)
t=1    Creates mycluster-2 (Pending)                            mycluster-1 (Pending)
                                                                mycluster-2 (Pending)
t=2    Creates mycluster-3 (Pending)                            mycluster-1 (Pending)
                                                                mycluster-2 (Pending)
                                                                mycluster-3 (Pending)
t=3                                   mycluster-1 → InProgress
t=10                                  mycluster-1 → Applied
t=11                                  mycluster-2 → InProgress
t=12   Periodic: deletes                                        mycluster-2 (InProgress)
       mycluster-1 (superseded)                                 mycluster-3 (Pending)
t=20                                  mycluster-2 → Applied
t=21                                  mycluster-3 → InProgress
t=22   Periodic: deletes                                        mycluster-3 (InProgress)
       mycluster-2 (superseded)
t=30                                  mycluster-3 → Applied
       ← only mycluster-3 remains (current baseline) →
```

#### Status aggregation

The Operator does **not** mirror the full `RedkeyClusterConfig.status` verbatim. Instead, it computes a simplified, user-facing status for `RedkeyCluster.status` based on the highest-sequence config:

```yaml
status:
  phase: <string>         # Configuring | Ready | Error
  conditions:
    - type: Ready
      status: "True"/"False"
    - type: ConfigPending
      status: "True"/"False"
    - type: Error
      status: "True"/"False"
  lastUpdatedAt: <timestamp>
  observedGeneration: <int>
```

Mapping rules:

| Highest-sequence config state | `RedkeyCluster.status.phase` | `ConfigPending` |
| ----------------------------- | ---------------------------- | --------------- |
| `configPhase: Pending` | `Configuring` | `True` |
| `configPhase: InProgress` | `Configuring` | `True` |
| `configPhase: Applied`, `phase: Ready` | `Ready` | `False` |
| `configPhase: Applied`, `phase: Error` | `Error` | `False` |
| `configPhase: Applied`, any other `phase` | `Configuring` | `False` |

The detailed operational status (`substatus`, `upgradingPartition`, `nodes` map, per-node replication details) remains exclusively in `RedkeyClusterConfig.status`. Users and tooling that need fine-grained visibility into cluster internals query the `RedkeyClusterConfig` directly; the `RedkeyCluster` CR provides only a high-level summary suitable for dashboards and alerts.

### 3.3 Redkey Operator (Umbrella Coordinator)

After the refactoring, the Operator operates through two distinct control paths:

#### Periodic reconciliation loop

The loop runs on a configurable interval and is responsible for infrastructure health checking and housekeeping:

1. **Ensure the Robin `Deployment` is healthy**  
   On every cycle the Operator verifies that the Robin `Deployment` exists and matches the desired spec derived from `spec.robin` in the `RedkeyCluster` CR. If the Deployment is missing it is recreated; if the spec has drifted (e.g. a new Robin image or updated resource limits) it is updated, triggering a standard Kubernetes rolling update. The full override capability present today (`spec.robin.template` for pod-level customisations, `spec.robin.config` for Robin's operational settings) is preserved.

2. **Aggregate status from `RedkeyClusterConfig` → `RedkeyCluster`**  
   On every cycle the Operator reads the status of the **highest-sequence** `RedkeyClusterConfig` and computes the simplified `RedkeyCluster.status` (`phase` + conditions) according to the mapping rules defined in Section 3.2 (Status aggregation). The `RedkeyCluster` CR remains the high-level entry point for end users and alerting, while detailed operational data lives in `RedkeyClusterConfig`.

3. **Clean up superseded `RedkeyClusterConfig` CRs**  
   On every cycle the Operator lists all `RedkeyClusterConfig` CRs for each cluster. If the highest-sequence config has `configPhase: Applied`, all older `Applied` configs are deleted. This keeps the namespace clean.

#### Event-driven watch on `RedkeyCluster`

In addition to the periodic loop, the Operator maintains a watch on `RedkeyCluster` CR events. Whenever a create, update, or delete event is received it reacts immediately:

1. **Manage Robin's RBAC resources**  
   On creation, the Operator creates a dedicated `ServiceAccount`, a `Role` (or `ClusterRole`, depending on the namespace scope required), and a `RoleBinding` granting Robin the permissions it needs to manage K8s resources within the cluster's namespace. All these resources carry `ownerReferences` pointing to the `RedkeyCluster` CR, so they are automatically garbage-collected by Kubernetes when the CR is deleted, leaving the namespace clean.

2. **Create new `RedkeyClusterConfig`**  
   On every relevant change event, the Operator increments the sequence counter (stored as annotation `redkey.inditex.com/config-sequence` on `RedkeyCluster`) and **creates a new** `RedkeyClusterConfig` CR named `<cluster-name>-<sequence>`. The new CR carries the full desired cluster state computed from the CR spec, including the raw redis configuration from `spec.config` and the auth Secret reference from `spec.auth`. Robin can then read them and build the final `redis.conf` ConfigMap autonomously. Robin is responsible for merging user config with defaults, computing memory limits, and reading the referenced Secret to add the auth directives — all at runtime, without the Operator ever touching the Secret contents.

The Operator no longer creates or manages StatefulSets, Services, PodDisruptionBudgets, redis.conf ConfigMaps, or Robin ConfigMaps. It no longer calls any HTTP API.

### 3.4 Redkey Robin (Full Business Logic Owner)

Redkey Robin becomes the self-contained operator for a single `RedkeyCluster` instance. It takes over everything that the Operator used to do at the K8s resource level plus everything it was doing at the Redis level.

Rather than using a Kubernetes informer/watch on `RedkeyClusterConfig` CRs, Robin operates through a **single reconciliation polling loop** running on a configurable interval. This simplifies the component — no watch setup, no informer cache, no event channel plumbing — while still guaranteeing convergence.

#### Reconciliation loop

On every tick of the loop, Robin executes the following steps in order:

1. **List `RedkeyClusterConfig` CRs** — Robin lists all CRs with label `redkey.inditex.com/cluster: <cluster-name>` via a standard `GET` (no watch). It sorts them by `spec.sequence`.

2. **Process pending configs** — If a config is currently `InProgress`, continue reconciling it until complete. Otherwise:
   - Among all `Pending` configs, select the one with the lowest `spec.sequence`.
   - If `skipIfSuperseded` is `true` on that config and a higher-sequence `Pending` config exists, mark it as `Applied` (superseded) without executing changes, and move to the next.
   - Otherwise, set it to `InProgress` and begin reconciliation (create/update K8s resources, drive Redis operations).
   - When reconciliation completes, set it to `Applied`. This config is now the current baseline.
   - If more `Pending` configs remain, continue immediately with the next one (no wait between consecutive configs).

3. **Verify Kubernetes resources** — Using the current baseline config (highest-sequence `Applied` CR), Robin verifies that all managed K8s objects exist and match the expected state:
   - `StatefulSet` — correct replicas, image, resource limits, volume claims
   - `Service` — correct selector and ports
   - `PodDisruptionBudget` — correct selector and `minAvailable`/`maxUnavailable`
   - `ConfigMap` (`redis.conf`) — correct content (merged from `spec.redisConfig`, defaults, auth)

   If any object is missing or has drifted, Robin recreates or patches it immediately.

4. **Verify and repair Redis cluster health** — Robin checks the live Redis/Valkey cluster and, if any issue is detected, attempts to fix it autonomously:
   - **Node membership** — Verifies all expected nodes are part of the cluster. If a node is missing (e.g. after a pod restart with a new IP), Robin runs `CLUSTER MEET` to re-introduce it. If stale nodes are present (e.g. leftover entries from a previous scale-down), Robin runs `CLUSTER FORGET` to remove them.
   - **Cluster integrity** — Runs the equivalent of `CLUSTER FIX` to resolve inconsistent states (e.g. nodes stuck in importing/migrating state, open slots).
   - **Slot coverage** — Ensures all 16384 slots are assigned. If any slots are unassigned (e.g. after a node loss), Robin reassigns them to available primaries.
   - **Replication** — Verifies each primary has the expected number of replicas. If a replica is missing or a primary has too few replicas, Robin runs `CLUSTER REPLICATE` to restore the expected topology.
   - **Balance** — Checks that slots are distributed evenly across primaries according to the expected distribution.

   If the cluster is **unbalanced** (e.g. after a failover, a node restart, or a previous incomplete scaling), Robin triggers a rebalance operation. It sets `status.phase: Rebalancing` on the active `RedkeyClusterConfig` and performs slot migration until the cluster reaches the expected distribution. Once complete, it transitions back to `Ready`.

5. **Status reporting** — At the end of each cycle, Robin writes the current cluster status to the active `RedkeyClusterConfig.status` subresource (both `configPhase` and operational `phase`).

6. **Wait** — If no pending configs remain and all checks passed, Robin sleeps for the configured poll interval before the next cycle.

#### Polling interval behaviour

The poll interval is configurable via `spec.robinConfig.reconcileInterval` in `RedkeyClusterConfig`. Robin uses an **adaptive** strategy:

- **Busy mode** — When processing pending configs or performing corrective actions (rebalance, resource recreation), Robin loops immediately without waiting.
- **Idle mode** — When no pending configs exist and all health checks pass, Robin waits the full configured interval before polling again.

This ensures fast reaction to new configs while avoiding unnecessary API calls during steady state.

#### Full Kubernetes resource lifecycle

Robin creates, updates, and deletes:

- `ConfigMap` with `redis.conf`: Robin reads `spec.redisConfig` and `spec.auth.secretName` from the `RedkeyClusterConfig`, merges the user-provided directives with defaults, computes memory limits, reads the auth Secret, and produces the final `redis.conf`. This approach keeps Secret contents out of the CRD and concentrates all redis configuration logic in Robin.
- `StatefulSet` for Redis/Valkey pods
- Headless `Service`
- `PodDisruptionBudget`

#### Cluster state machine

Robin drives its own cluster from `Initializing` through `Configuring`, `Ready`, and all transition states (`ScalingUp`, `ScalingDown`, `Upgrading`, `Rebalancing`, `Error`, `Maintenance`). The `Rebalancing` state is new — it is entered when the periodic health check detects an unbalanced slot distribution and Robin autonomously redistributes slots.

#### REST API removal

The HTTP server, all handlers, and the OpenAPI spec are removed. The `internal/httpserver` package is deleted.

#### Metrics poller

Unchanged. Robin continues to expose Prometheus metrics at `/metrics`.

### 3.5 Proposed Communication Flow

```ascii
┌──────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                           │
│                                                                      │
│  User                                                                │
│   │  apply RedkeyCluster CR                                          │
│   ▼                                                                  │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │                       Redkey Operator                        │    │
│  │  - Watches RedkeyCluster CR events                           │    │
│  │  - Creates new RedkeyClusterConfig per change                │    │
│  │  - Manages Robin Deployment (periodic)                       │    │
│  │  - Mirrors highest-sequence status → RedkeyCluster (periodic)│    │
│  │  - Cleans up superseded configs (periodic)                   │    │
│  └──────────────────────────────────────────────────────────────┘    │
│       │ creates new CRs       ▲ reads status    │ deletes old CRs    │
│       ▼                       │                 ▼                    │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │             RedkeyClusterConfig CRs (sequenced)              │    │
│  │  mycluster-1 (Applied)  ← current baseline                   │    │
│  │  mycluster-2 (Pending)  ← next change                        │    │
│  │  mycluster-3 (Pending)  ← queued change                      │    │
│  └──────────────────────────────────────────────────────────────┘    │
│               │ polls (list by label)      │ writes status           │
│               ▼                            │                         │
│          ┌──────────────────────────────────────┐                    │
│          │            Redkey Robin              │                    │
│          │  - Polls on configurable interval    │                    │
│          │  - Processes Pending configs         │                    │
│          │  - Verifies K8s resources + Redis    │                    │
│          │  - Rebalances if needed              │                    │
│          │  - Exposes Prometheus metrics        │                    │
│          └──────────────────────────────────────┘                    │
│               │ creates/manages                                      │
│               ▼                                                      │
│  ┌────────────────────────────────────────────────────────┐          │
│  │   StatefulSet │ Service │ PDB │ redis.conf ConfigMap   │          │
│  └────────────────────────────────────────────────────────┘          │
│               │                                                      │
│               ▼                                                      │
│  ┌──────────────────────────┐                                        │
│  │   Redis/Valkey Cluster   │                                        │
│  └──────────────────────────┘                                        │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 4. Changes by Repository

### 4.1 Redkey Operator Changes

#### Removed

| Component | File(s) | Reason |
| --------- | ------- | ------ |
| StatefulSet management | `controllers/kubernetes.go` | Moved to Robin |
| Service management | `controllers/kubernetes.go` | Moved to Robin |
| PodDisruptionBudget management | `controllers/pdb.go` | Moved to Robin |
| Redis ConfigMap creation (`redis.conf`) | `controllers/kubernetes.go` | Moved to Robin |
| Robin ConfigMap creation (runtime config) | `controllers/kubernetes.go` | Moved to Robin |
| Robin HTTP client | `controllers/robin.go` | Communication via CRD |
| Redis manager | `controllers/redis_manager.go` | Communication via CRD |
| Full status state machine | `controllers/redkeycluster_manager.go` | Moved to Robin |
| `reconcileStatusInitializing/Configuring/Upgrading/ScalingUp/ScalingDown/Error/Maintenance` | `controllers/redkeycluster_manager.go` | Moved to Robin |

#### Modified

| Component | Change |
| --------- | ------ |
| `controllers/redkeycluster_reconciler.go` | Simplified to: (1) ensure Robin `Deployment` exists and matches desired spec, (2) ensure Robin `ServiceAccount`, `Role`, and `RoleBinding` exist, (3) create new `RedkeyClusterConfig` CR on each `RedkeyCluster` change (sequenced), (4) read highest-sequence `RedkeyClusterConfig` status and mirror it to `RedkeyCluster` status, (5) clean up superseded `Applied` configs |
| `controllers/kubernetes.go` | Retains only Robin `Deployment` and RBAC resource builders; all other resource builders moved to Robin |
| `controllers/redkeycluster_manager.go` | Retains only resource finders for the Robin `Deployment`, `ServiceAccount`, `Role`, and `RoleBinding`, sequence counter management, and status aggregation helpers |
| `controllers/conditions.go` | Retains condition management for `RedkeyCluster` status mirroring |
| `api/v1/redkeycluster_types.go` | Spec unchanged (user-facing API is stable); `spec.robin.template` and `spec.robin.config` remain as the authoritative source for Robin deployment configuration |
| `cmd/main.go` | Registers the `RedkeyClusterConfig` CRD scheme and watcher; removes Robin HTTP client configuration |

#### Added

| Component | Description |
| --------- | ----------- |
| `api/v1/redkeyclusterconfig_types.go` | New CRD type definition for `RedkeyClusterConfig` including `spec.sequence`, `spec.skipIfSuperseded`, and `status.configPhase` |
| `api/v1/zz_generated.deepcopy.go` | Regenerated to include `RedkeyClusterConfig` |
| `config/crd/bases/redkeyclusterconfig_crd.yaml` | Generated CRD manifest |
| `controllers/rbac.go` | New file containing builders for the per-cluster `ServiceAccount`, `Role`, and `RoleBinding` resources |

### 4.2 Redkey Robin Changes

#### Removed

| Component | File(s) | Reason |
| --------- | ------- | ------ |
| HTTP server | `internal/httpserver/` (entire package) | Communication via CRD |
| REST API handlers | `internal/httpserver/handlers.go` | Replaced by CRD watch |
| Request/response types | `internal/httpserver/requests.go`, `responses.go` | No longer needed |
| OpenAPI spec | `api/openapi-rest.yml` | No longer needed |

#### Modified

| Component | Change |
| --------- | ------ |
| `cmd/main.go` | Removes HTTP server bootstrap; adds Kubernetes client; starts the reconciliation polling loop; Robin's configuration is now read from the CRD spec instead of a ConfigMap file |
| `internal/config/config.go` | Configuration source changes from a mounted ConfigMap file to the `RedkeyClusterConfig` spec; the `Config` struct is reconciled on each poll cycle |
| `internal/reconciler/reconciler.go` | Implements the polling loop: list CRs, process pending configs sequentially, verify K8s resources, verify Redis cluster health, trigger rebalance if needed, write status |
| `internal/reconciler/cluster.go` | Expands to include K8s resource management (StatefulSet, Service, PDB, redis.conf ConfigMap) and drift detection/correction |

#### Added

| Component | Description |
| --------- | ----------- |
| `internal/kubernetes/` | New package containing K8s client setup, resource management functions (StatefulSet, Service, PDB, ConfigMap builders), and drift detection logic ported from the Operator's `controllers/kubernetes.go` |
| `internal/status/` | New package responsible for writing reconciliation results back to `RedkeyClusterConfig.status`, including `configPhase` transitions |
| `internal/sequence/` | New package implementing the sequenced config processing logic: sorting, skip-if-superseded evaluation, and next-config selection |
| `internal/healthcheck/` | New package implementing K8s resource verification (expected vs actual state) and Redis cluster health checks (slot coverage, balance, replication) |

#### Robin Deployment Configuration

The Robin `Deployment` continues to be built from `spec.robin` in the `RedkeyCluster` CR, which is the single user-facing place to configure Robin:

- **`spec.robin.template`** (PodTemplateSpec): image, resource requests/limits, environment variables, node affinity, tolerations, sidecar containers, and any other pod-level overrides — exactly as supported today.
- **`spec.robin.config`**: Robin's operational settings (reconciliation intervals, health probe periods, retry parameters, etc.), which the Operator passes through to the `RedkeyClusterConfig` spec so that Robin can read them from the CRD.

The Operator sets `ownerReferences` on the Robin `Deployment` pointing to the `RedkeyCluster` CR, so the Deployment is automatically garbage-collected when the cluster is deleted.

---

## 5. Justification

### Cohesion and Single Responsibility

In the current architecture, the knowledge of how to run a Redkey cluster is split: the Operator knows about K8s topology, Robin knows about Redis operations. Every new cluster capability (e.g., a new storage option, a new upgrade strategy) must be coordinated across both components. After the refactoring, Robin is the single component that holds all cluster knowledge. Changes to cluster behaviour are localised.

### Elimination of Synchronous HTTP Coupling

The current REST API introduces synchronous, network-dependent coupling between two Kubernetes components. This requires Robin to be reachable for the Operator to make progress, introduces retry logic, timeout handling, and API versioning overhead, and makes it harder to reason about failure modes. The CRD-based model is fully asynchronous and declarative — the Operator writes desired state and moves on; Robin converges to that state in its own time.

### Operator Simplification and Scalability

Once relieved of per-cluster K8s resource management and REST API consumption, the Operator's reconciliation loop becomes a simple spec translation and status mirror. A single Operator instance can efficiently coordinate hundreds of Redkey clusters across large Kubernetes environments without being a bottleneck.

### Cluster Autonomy and Resilience

With Robin owning all cluster logic, a Redkey cluster becomes fully autonomous. If the Operator pod is unavailable (rolling update, node failure, maintenance), running clusters are completely unaffected. Robin continues to reconcile, heal, and report status. The Operator is only needed to apply new desired state changes.

### Testability

Both components become independently testable:

- **Redkey Operator** can be tested by verifying that it produces the correct `RedkeyClusterConfig` spec and Robin `Deployment` from a given `RedkeyCluster` CR, with no need for a running Robin.
- **Redkey Robin** can be tested by directly creating or patching a `RedkeyClusterConfig` in a test environment (or using envtest), without needing the Operator to generate it. The full cluster lifecycle can be exercised this way.

### Independent Component Upgrades

The new architecture significantly simplifies version management for both components.

**Redkey Operator upgrades** have no impact on running clusters. Because the Operator no longer participates in the real-time lifecycle of Redis/Valkey clusters — only Robin does — the Operator pod can be updated, restarted, or temporarily unavailable without affecting any running cluster. Existing `RedkeyClusterConfig` objects and Robin pods continue operating normally during and after the Operator upgrade.

**Redkey Robin upgrades** are performed declaratively by updating `spec.robin.template` in the `RedkeyCluster` CR (e.g. bumping the container image tag). The Operator detects the change in its next reconciliation cycle, updates the Robin `Deployment`, and Kubernetes performs a rolling update of the Robin pod. Since Robin and the Redis/Valkey cluster are different pods, the rolling update of Robin is fully transparent to the cluster it manages.

The existing support for fine-grained configuration overrides via `spec.robin.template` (PodTemplateSpec overrides) is fully preserved, giving platform teams control over node affinity, tolerations, extra environment variables, sidecar containers, and any other pod-level customisations independently of the Redis cluster configuration.

### Auditable Change History

The sequenced `RedkeyClusterConfig` model provides a built-in audit trail: each configuration change is a discrete, immutable CR with its own lifecycle (`Pending → InProgress → Applied`). While the active config is always the latest `Applied` CR, the sequence of changes can be observed in real time via `kubectl get redkeyclusterconfigs -l redkey.inditex.com/cluster=<name>`. This is a significant improvement over in-place updates, where the previous desired state is lost the moment a new one is written.

---

## 6. Impact Assessment

| Area | Impact |
| ---- | ------ |
| **User-facing API** | `RedkeyCluster` **spec** is unchanged. `RedkeyCluster` **status** is simplified: `phase` collapses from the current values (`Initializing`, `Configuring`, `Ready`, `ScalingUp`, `ScalingDown`, `Upgrading`, `Error`, `Maintenance`) to three values (`Configuring`, `Ready`, `Error`); `substatus` and `nodes` map are removed (available only in `RedkeyClusterConfig.status`); conditions are reduced to `Ready`, `ConfigPending`, and `Error`. Existing consumers of the detailed status must migrate to querying `RedkeyClusterConfig`. |
| **Helm charts** | `redkey-operator` chart: simplify Deployment spec (no Robin HTTP address needed). `redkey-cluster` chart: add `RedkeyClusterConfig` CRD and Robin's new RBAC rules. |
| **RBAC — Robin (per-cluster `Role`)** | The Operator creates a dedicated `ServiceAccount`, `Role`, and `RoleBinding` per cluster, all owned by the `RedkeyCluster` CR (auto-deleted on CR removal). The `Role` grants Robin: `get/list/watch/create/update/patch/delete` on StatefulSets, Services, ConfigMaps, PodDisruptionBudgets; `get` on Secrets (read-only, for auth); `get/list/watch` on `RedkeyClusterConfig` (spec read); `update/patch` on `RedkeyClusterConfig/status` only. Robin is explicitly **denied** write access to `RedkeyClusterConfig` (main resource) to prevent it from altering the desired spec. |
| **RBAC — Operator (`ClusterRole`)** | The Operator's own `ClusterRole` must be reviewed. It gains: `create/delete` on `ServiceAccounts`, `Roles`, and `RoleBindings` (to manage per-cluster RBAC); `create/delete` on `RedkeyClusterConfig` (main resource — create new configs, delete superseded ones); `get/list/watch` on `RedkeyClusterConfig/status`. It is explicitly **not** granted `update/patch` on `RedkeyClusterConfig/status` to prevent it from overwriting Robin's reported state. REST API network policy rules can be removed. |
| **Observability** | Robin's Prometheus `/metrics` endpoint is unchanged. The new `RedkeyClusterConfig` status field provides a native `kubectl`-observable state without additional tooling. Multiple sequenced `RedkeyClusterConfig` CRs per cluster provide real-time visibility into the change queue. |
| **Upgrade path** | A migration step is needed to create an initial `RedkeyClusterConfig` (sequence 1, `configPhase: Applied`) for existing clusters and transfer ownership of K8s resources (StatefulSet, Service, etc.) to Robin via `ownerReferences`. This can be handled by an upgrade job or by a one-time migration reconciliation pass in the new Operator version. |
| **Backwards compatibility** | The REST API removal is a **breaking change** for any external consumers of Robin's HTTP API. A deprecation notice and audit of any existing integrations is required before removal. |
