# Redis Cluster Robin

The Redis Cluster CRD provides the field `spec.robin` to deploy the Redis Cluster Robin, a faithful partner who assists the operator in the dangerous Gotham.
Robin is designed to help the Operator (Batman) in its duties, in particular:

- Provide Redis Cluster prometheus metrics
- FUTURE WORK

The Operator deploys a Deployment and a ConfigMap for Robin given the configuration provided in `spec.robin` for each Redis Cluster, if configured. The operator is responsible 
of reconcile any addition, update or delete in the `spec.robin` of a RedisCluster.

## How to deploy Robin

Robin deployment can be configured in `spec.robin.template`. This field is an object representing a [PodSpecTemplate](https://github.com/kubernetes/kubernetes/blob/v1.32.2/staging/src/k8s.io/api/core/v1/types.go#L5050). The template is then used by the Redis Operator to create, update or delete a Deployment with Robin, whose name is `<RedisClusterName>-robin`.

Robin connects to all the nodes of the Redis Cluster using port 6379 and the K8s Redis Pod domain name (e.g.: rediscluster-sample-0.redis-cluster-sample). Therefore, a DNS resolving that name 
to the Pod IP is needed for Robin to work.

### Example

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedisCluster
...
spec:
  ...
  robin:
    template:
      ...
      spec:
        containers:
          - image: 'redis-robin:0.0.1'
            name: robin
            imagePullPolicy: Always
            ports:
              - containerPort: 8080
                name: prometheus
                protocol: TCP
            volumeMounts:
              - mountPath: /opt/conf/configmap
                name: rediscluster-sample-robin-config
        volumes:
          - configMap:
              defaultMode: 420
              name: rediscluster-sample-robin
            name: rediscluster-sample-robin-config
```

## How to configure Robin

Robin configuration can be included in `spec.robin.config`. This field is an string whose content is included in the key `application-configmap.yml` of the ConfigMap `<RedisClusterName>-robin`. 
The content is expected to be a valid YAML with several fields which can be seen in [Configuration fields](#configuration-fields) section

The Redis Operator applies the MD5 algorithm to the `spec.robin.config` content and adds the result in the `checksum/config` annotation of the Robin Deployment template. This way, any change 
in the configuration content will trigger a Robin POD recreation, which will have always the latest content applied to the RedisCluster object.

### Configuration fields

The expected fields of the `spec.robin.config` YAML are:

- `metadata`: object with the labels that will be added to the Prometheus metrics
- `redis`: object with the redis cluster configuration:
  - `operator`: 
    - `collection_interval_seconds` (int): sleep time in seconds between two consecutive metrics polling iterations.
  - `cluster`:
    - `replicas` (int): number of nodes of the Redis Cluster. Used to infer the Redis node domain name.
    - `name` (string): Redis Cluster name.
    - `namespace` (string): K8s namespace of the Redis Cluster.
    - `health_probe_interval_seconds` (int): 
    - `healing_time_seconds` (int): 
    - `max_retries` (int): maximum retries to connect to a Redis node.
    - `back_off` (time.Duration): sleep time between two consecutive attempts to connect to a Redis node.
  - `metrics`: 
    - `version`: Redis metrics version.
    - `redis_info_keys`: Redis info keys that are asked to each Redis node and are exported in the Prometheus metrics.

### Example

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedisCluster
...
spec:
  ...
  robin:
    ...
    config: |
      metadata:
        application: showpaas
        version: "7.2.4"
        environment: des
        tenant: global
        domain: swdelivery
        slot: sample
        layer: middleware-redis
        namespace: redis-operator
        platformid: "meccanoarteixo2"
        service: "showpaas"
      redis:
        operator:
          collection_interval_seconds: 60
        cluster:
          replicas: 1
          name: "rediscluster-sample"
          namespace: redis-operator
          health_probe_interval_seconds: 60
          healing_time_seconds: 60
          max_retries: 2
          back_off: 10s
        metrics:
          version: 0.10.2.0
          redis_info_keys:
            - keyspace_hits
            - evicted_keys
            - connected_clients
            - total_commands_processed
            - keyspace_misses
            - expired_keys
            - redis_version
            - used_memory_rss
            - maxmemory
            - used_cpu_sys
            - used_cpu_sys_children
            - used_cpu_user
            - used_cpu_user_children
            - total_net_input_bytes
            - total_net_output_bytes
            - aof_base_size
            - aof_current_size
            - mem_aof_buffer
```

## How to develop Robin

Please refer to [Redis Robin](../developer-guide.md#redis-robin) section of the Developer Guide to know how to develop, build and deploy Robin for development and debugging purposes.
