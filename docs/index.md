# Smart Proxy Documentation

Welcome to the **Smart Proxy** documentation. This project provides an intelligent reverse proxy for Kubernetes and OpenShift environments, enabling advanced features like auto-sleeping deployments, dependency management, and seamless routing.

## Key Features

- **Auto-Sleep & Wake**: Automatically scale down inactive deployments to save resources and checking internal traffic to wake them up on demand.
- **Dependency Management**: Define chains of dependent services that start/stop together.
- **Unified Patching**: Manage both Kubernetes Ingresses and OpenShift Routes from a single interface.
- **Admin Dashboard**: A modern React-based UI for monitoring and configuration.
- **Observability**: Real-time logs and status updates.

## Navigation

- [Installation Guide](installation.md) - How to deploy Smart Proxy.
- [Architecture](architecture.md) - How it works under the hood.
- [Configuration](configuration.md) - detailed configuration options.
