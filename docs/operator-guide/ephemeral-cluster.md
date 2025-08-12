# Ephemeral Mode / Zero Persistent Volume Claims

Ephemeral mode, also known as Zero Persistent Volume Claims (PVCs), disables persistent volume claims for storing the `redis.conf` configuration file. This mode frees up storage, removes the need for managing persistent volume claims and decreases pod start-up time. When using Redis as a cache it is recommended to enable ephemeral mode. Ephemeral mode is supported in RedKey Operator 0.2.27 for new clusters.

## How To Enable Ephemeral Mode

For a new cluster configuration set the property `ephemeral: true` and apply the configuration. See the following snippet:

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedKeyCluster
metadata:
  name: redis-cluster
  ...
spec:
  ...
  ephemeral: true
```

## Limitation: Ephemeral mode only works on new Redis clusters

Currently it is not possible to change a Redis cluster from persistent to ephemeral without downtime by restarting a statefulset. This means performance will be reduced temporarily when deleting the persistent cluster and replacing it with a new ephemeral cluster. The reason why is that the existing statefulset of a persistent cluster has `VolumeClaimTemplates` configured. These templates cannot be removed at runtime via a patch command. See [this Kubernetes issue](https://github.com/kubernetes/kubernetes/issues/65870). If you would remove the persistent volumes, volume claims and pods they would all be recreated by the statefulset. The only option is to delete the statefulset but that will remove all pods. In that case you may as well create a new cluster. What would be possible is replicating data between the persistent and ephemeral cluster either through a script or via the Redis Enterprise Operator using replica-of.
