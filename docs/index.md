# Smart Proxy Documentation

Welcome to the **Smart Proxy** documentation. This project provides an intelligent reveSmart Proxy sits between your users and your applications, enabling advanced features like auto-sleeping deployments, dependency chains, and unified route patching.

<p align="center">
  <img src="https://raw.githubusercontent.com/ivseb/smart-proxy/main/media/logo.png" alt="Smart Proxy Logo" width="200"/>
</p>

![Smart Proxy Demo](https://raw.githubusercontent.com/ivseb/smart-proxy/main/media/SmartProxy.gif)

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
