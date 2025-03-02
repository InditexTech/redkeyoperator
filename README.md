# ![Redis Operator icon](docs/images/badge.png) Redis Operator for Kubernetes

The easiest way to deploy and manage a Redis cluster in Kubernetes implementing the [operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

This operator implements a controller that extends the Kubernetes API allowing to seamlessly deploy a Redis cluster, monitor the deployed resources implementing a reconciliation loop, logging events, manage cluster scaling and recover from errors.

Redis operator is built using [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder) and [operator-sdk](https://github.com/operator-framework/operator-sdk).

## Features

* Redis cluster creation
* Redis cluster fixing
* Slots allocation
* PVC
* Sidecar pods support (e.g. custom metrics applications)
* Persistence
* RedisGraph support

## Documentation

Refer to [operator guide](./docs/operator-guide/toc.md) to have an overview of the main Redis configuration and management options, and a troubleshooting guide.

If you are a developer, you'll find interesting information in the [developer guide](./docs/developer-guide.md).

## Versions

* Go version: v1.24.0
* Operator SDK version: v1.37.0


## License

Copyright 2025 Inditex.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License. You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions and limitations under the License.
