# Smart Proxy

**Intelligent Request Handling for Kubernetes Ingresses and OpenShift Routes.**

Smart Proxy sits between your users and your applications, enabling advanced traffic management features like auto-sleeping deployments, dependency chains, and unified route patching.

![Smart Proxy Demo](media/SmartProxy.gif)

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/smart-proxy)](https://artifacthub.io/packages/search?repo=smart-proxy)
[![Docker Pulls](https://img.shields.io/docker/pulls/isebben/smart-proxy)](https://hub.docker.com/r/isebben/smart-proxy)
[![License](https://img.shields.io/github/license/ivseb/smart-proxy)](https://github.com/ivseb/smart-proxy/blob/main/LICENSE)
[![GitHub release (latest by date)](https://img.shields.io/github/v/release/ivseb/smart-proxy)](https://github.com/ivseb/smart-proxy/releases)

## Features

- **Auto-Sleep & Wake:** Automatically scale down inactive deployments to 0 replicas. Incoming traffic wakes them up on demand, showing a "Waking Up" page to the user.
- **Dependency Management:** Define complex dependency chains (e.g., App A depends on DB B). Smart Proxy ensures all dependencies are running before properly routing traffic.
- **Unified Interface:** A modern React-based Admin Dashboard to manage both Kubernetes Ingresses and OpenShift Routes.
- **Observability:** Real-time system logs and status indicators.
- **Configurable:** persistence via annotations, configurable proxy port (`SMART_PROXY_PORT`), and more.

## Quick Start

### 1. Local Development (Docker Desktop)

We provide a script to set up a full local environment with Smart Proxy and a demo application.

```bash
./scripts/setup-local.sh
```

- **Dashbaord:** [http://admin.local](http://admin.local) (Add `127.0.0.1 admin.local` to `/etc/hosts`)
- **Demo App:** [http://frontend.local](http://frontend.local) (Add `127.0.0.1 frontend.local` to `/etc/hosts`)

### 2. Helm Installation

```bash
helm install smart-proxy ./charts/smart-proxy --namespace smart-proxy --create-namespace
```

## Documentation

- [Installation Guide](docs/installation.md)
- [Architecture Overview](docs/architecture.md)
- [Configuration Reference](docs/configuration.md)

## License

MIT
