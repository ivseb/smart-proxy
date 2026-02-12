# Installation Guide

## Prerequisites

- Kubernetes 1.24+ or OpenShift 4.x
- Helm 3.x
- specialized ServiceAccount permissions (handle by Helm chart)

## Installing via Helm

1.  Clone the repository:
    ```bash
    git clone https://github.com/your-org/smart-proxy.git
    cd smart-proxy
    ```

2.  Install the chart:
    ```bash
    helm install smart-proxy ./charts/smart-proxy --namespace smart-proxy-system --create-namespace
    ```

3.  Verify installation:
    ```bash
    kubectl get pods -n smart-proxy-system
    ```

## Installing via Docker (Local Development)

We provide a script to set up a local development environment using Docker Desktop's Kubernetes.

1.  Ensure Docker Desktop is running with Kubernetes enabled.
2.  Run the setup script:
    ```bash
    ./scripts/setup-local.sh
    ```
    This script will:
    - Build the Docker image locally.
    - Deploy the Smart Proxy, Redis, and a demo Frontend/Backend application.
    - Configure local Ingress resources.

3.  Access the Dashboard:
    - Add `127.0.0.1 admin.local` to your `/etc/hosts`.
    - Visit `http://admin.local`.

## Docker Image

The Docker image is available on Quay.io (example):
```bash
docker pull quay.io/your-user/smart-proxy:latest
```
