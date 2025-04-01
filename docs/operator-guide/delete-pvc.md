# Deletion of PVCs on successful scale-down and cluster deletion

The PVCs (Persisteng Volume Claims) of the Redis nodes not only holds its data, but also its configuration, including its knowledge about the other nodes in the cluster. While this is an expected behaviour for persistent clusters, it creates issues especially in development environments where people tend to delete and create clusters and scale up and down frequently.

Let's think a scenario where you delete a 5 master cluster and create a 3 master cluster with the same name, 3 pods will be created and will have the configuration files and data from the first 3 pods of the 5 master cluster. So they will be expecting 2 more nodes which aren't there and have some slots missing.

Or you may have a 7 master cluster, scale it down to 3 and scale it up to 5 masters. When the cluster is scaled down to 3, all the slots will be moved to the first 3 nodes, they'll forget about the tailing 4 nodes and their configuration will change accorgingly. When you scale it back to 5 nodes, the 2 newly added nodes will load configuration where there were 7 nodes and this configuration will not match the first 3 nodes' configurations.

Starting with Redis Operator release 0.2.33, Redis Operator supports deletion of PVCs after a scale down operation or a cluster deletions. In case of scaling down, the operator will delete de PVCs of the nodes that are deleted, and in case of cluster deletion, it'll delete all the PVCs owned by the nodes of the cluster. This feature can be enabled by setting `deletePVC` to `true` under `spec` section of a RedisCluster definition.

> This setting is optional. The PVCs belonging to the cluster without this setting is its manifest will not be deleted.

> There's a [feature](https://kubernetes.io/blog/2021/12/16/kubernetes-1-23-statefulset-pvc-auto-deletion/) of Kubernetes which optionally deletes PVCs of StatefulSets upon scaling down or deletion introduced in version 1.23. By the time we developed our own feature, this feature was still in Alpfa status.

## Example

```yaml
apiVersion: redis.inditex.com/v1alpha1
kind: RedisCluster
...
spec:
  ...
  deletePVC: true
```