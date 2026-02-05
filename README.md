<!--
SPDX-FileCopyrightText: 2024 INDUSTRIA DE DISEÑO TEXTIL S.A. (INDITEX S.A.)

SPDX-License-Identifier: CC-BY-SA-4.0
-->

<div align="center">

# Redkey Operator

**Seamless Redkey Cluster Management on Kubernetes**

[![GitHub License](https://img.shields.io/github/license/InditexTech/redkeyoperator)](LICENSE)
[![GitHub Release](https://img.shields.io/github/v/release/InditexTech/redkeyoperator)](https://github.com/InditexTech/redkeyoperator/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/InditexTech/redkeyoperator)](go.mod)
[![Build Status](https://img.shields.io/github/actions/workflow/status/InditexTech/redkeyoperator/ci.yml?branch=main)](https://github.com/InditexTech/redkeyoperator/actions)

[![Kubernetes](https://img.shields.io/badge/Kubernetes-326CE5?style=flat&logo=kubernetes&logoColor=white)](https://kubernetes.io/)
[![Operator SDK](https://img.shields.io/badge/Operator%20SDK-326CE5?style=flat&logo=kubernetes&logoColor=white)](https://sdk.operatorframework.io/)
[![Go](https://img.shields.io/badge/Go-00ADD8?style=flat&logo=go&logoColor=white)](https://golang.org/)
[![REUSE Compliance](https://img.shields.io/badge/REUSE-compliant-green)](https://reuse.software/)

[![GitHub Issues](https://img.shields.io/github/issues/InditexTech/redkeyoperator)](https://github.com/InditexTech/redkeyoperator/issues)
[![GitHub Pull Requests](https://img.shields.io/github/issues-pr/InditexTech/redkeyoperator)](https://github.com/InditexTech/redkeyoperator/pulls)
[![GitHub Stars](https://img.shields.io/github/stars/InditexTech/redkeyoperator?style=social)](https://github.com/InditexTech/redkeyoperator/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/InditexTech/redkeyoperator?style=social)](https://github.com/InditexTech/redkeyoperator/network/members)

[🚀 Quick Start](#quick-start) • [📖 Documentation](./docs) • [🤝 Contributing](./CONTRIBUTING.md) • [📝 License](./LICENSE)

![Redkey Operator icon](docs/images/redkey-logo-50.png)

</div>

---

## Overview

A **Redkey Cluster** is a key/value cluster using either [Redis Official Image](https://hub.docker.com/_/redis) or [Valkey Official Image](https://hub.docker.com/r/valkey/valkey/) images to create its nodes (note that all cluster nodes must use the same image).

**Redkey Operator** is the easiest way to deploy and manage a Redkey Cluster in Kubernetes implementing the [operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

This operator implements a controller that extends the Kubernetes API allowing to seamlessly deploy a Redkey cluster, monitor the deployed resources implementing a reconciliation loop, logging events, manage cluster scaling and recover from errors.

Redkey operator is built using [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder) and [operator-sdk](https://github.com/operator-framework/operator-sdk).

## Key Features

- Redkey Cluster creation
- Cluster scaling up and down
- Cluster upgrading
  - Update node pods image
  - Update Redis configuration
  - Update node pods resources
- Ensure cluster health
- Slots allocation
- Ephemeral cluster (pure cache-like behavior) or using persistence
- RedisGraph support

## Quick Start

Prerrequisites:
- A running Kubernetes cluster (v1.24+)
- kubectl (v1.24+)
- Make

The operator can be installed using the provided Makefile. The following steps will guide you through the installation and deployment of a sample Redkey Cluster. Redkey Operator can be installed in any namespace, but for this quick start we will use the `redkey-operator` namespace.

1. Clone the repository to your local machine:

```bash
git clone https://github.com/InditexTech/redkeyoperator.git
cd redkeyoperator
```

2. Generate and install the CRDs:

```bash
make install
```

3. Deploy the operator in the cluster (replace `${VERSION}` with the desired version, e.g., `1.0.0`):

```bash
make deploy IMG=ghcr.io/inditextech/redkey-operator:${VERSION}
```

4. Create a Redkey Cluster (replace `${VERSION}` with the desired version, e.g., `1.0.0`):

```bash
make apply-rkcl IMG_ROBIN=ghcr.io/inditextech/redkey-robin:${VERSION}
```

## Documentation

Refer to [operator guide](./docs/operator-guide/toc.md) to have an overview of the main Redis configuration and management options, and a troubleshooting guide.

If you are a developer, you'll find interesting information in the [developer guide](./docs/developer-guide/toc.md).

Learn about [Redkey Cluster Status and Substatus](./docs/redkey-cluster-status.md).

Discover [Redkey Robin](./docs/redkey-robin.md).

The importance of the [purgeKeysOnRebalance](./docs/purge-keys-on-rebalance.md) parameter.

## Contributing

Contributions are welcome! Please read our [contributing guidelines](./CONTRIBUTING.md) to get started.

## Versions

- Go version (https://github.com/golang/go): v1.25.6
- Operator SDK version (https://github.com/operator-framework/operator-sdk): v1.42.0
- Kubernetes Controller Tools version (https://github.com/kubernetes-sigs/controller-tools): v0.20.0

## License

Copyright 2025 Inditex.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License. You may obtain a copy of the License at <http://www.apache.org/licenses/LICENSE-2.0>

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions and limitations under the License.
