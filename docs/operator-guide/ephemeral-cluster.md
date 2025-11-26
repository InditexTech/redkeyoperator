# Ephemeral Mode / Zero Persistent Volume Claims

Ephemeral mode, also known as Zero Persistent Volume Claims (PVCs), disables persistent volume claims for storing the `redis.conf` configuration file. This mode frees up storage, removes the need for managing persistent volume claims, and decreases pod start-up time. When using Redis as a pure cache, it is recommended to enable ephemeral mode.

## How To Enable Ephemeral Mode

For a new cluster configuration, set the property `ephemeral: true` and apply the configuration. See the following snippet:

```yaml
apiVersion: redis.inditex.dev/v1
kind: RedkeyCluster
metadata:
  name: redis-cluster
  ...
spec:
  ...
  ephemeral: true
```

## Limitation: Ephemeral mode can not be changed

Currently, it is not possible to change a Redkey cluster from persistent to ephemeral or vice versa. This is a  Statefulset limitation (see [this Kubernetes issue](https://github.com/kubernetes/kubernetes/issues/65870)).
