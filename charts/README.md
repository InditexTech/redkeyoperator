# Helm Charts

This directory contains Helm charts for deploying the various components of the project. Each subdirectory corresponds to a specific component and includes the necessary templates and configuration files for deployment.

## redkey-cluster Chart

Use this chart to deploy a Redkey cluster.

You can create a Readkey cluster by executindg the following command:

```bash
helm install my-redkey-cluster ./charts/redkey-cluster
```

This chart includes a **Helm post-install hook** that checks if the cluster is healthy after installation, waiting for `Condition "Ready"` to be true in the RedkeyCluster Custom Resource.

To use the chart with the post-install hook enabled, override the `waitReady.enabled` value to `true` in your `values.yaml` or via the command line:

```bash
# If you want to rollback on failure.
helm install my-redkey-cluster ./charts/redkey-cluster --set waitReady.enabled=true --rollback-on-failure

# If you want to skip rollback on failure.
helm install my-redkey-cluster ./charts/redkey-cluster --set waitReady.enabled=true
```
