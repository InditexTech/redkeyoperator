# Master-Replica Clusters

Starting with the release 0.2.34, RedKey Operator supports master-replica architecture of RedKey Cluster. This architecture increases resiliency of RedKey clusters as each master has one or more replicas and in case of a master goes down, its replica (or one of its replicas) can take over the role of its master and continue to serve without a downtime and data loss in practice. Theoretically, there will be a small downtime and a possibility of a data loss if a data on a master gets updated and master go down before the data change propagate to its replicas but these are negligible.

Clusters with master-replica architecture can be created by adding `replicas_per_master` specification and setting to an integer value inside the `spec` definition of a RedKey Cluster object. RedKey Operator will create replica nodes matching the count of `replicas_per_master` definition for each master nodes.

The minimum configuration for the correct operation of this function is:

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  replicas: 3
  replicas_per_master: 1
```

The `replicas` definition is the total number of Redis master nodes in the cluster. The `replicasPerMaster` definition is the number of slaves for each master node. The total number of Redis nodes in the cluster will be: (`replicas` x `replicasPerMaster`) + `replicas`.

If this feature is enabled, this additional configuration should be added to `redis.conf` to improve efficiency.

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  config:
    ...
    rdbcompression yes
    rdbchecksum no
    replica-priority 10
    repl-timeout 3
    repl-disable-tcp-nodelay yes
    replica-lazy-flush yes
    cluster-slave-validity-factor 1
    repl-backlog-ttl 120
    repl-backlog-size 256mb
```

## Examples

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  replicas: 3
  replicas_per_master: 2

```

RedKey Operator will create 6 pods, 3 masters and 3 replicas (one for each master).

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  replicas: 5
  replicas_per_master: 2
```

RedKey Operator will create 15 pods, 5 masters and 10 replicas (two for each master).

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  replicas: 5
  replicas_per_master: 0
```

RedKey Operator will create 5 pods, 5 masters and no replicas.

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
...
spec:
  ...
  replicas: 5
```

RedKey Operator will create 5 pods, 5 masters and no replicas.
