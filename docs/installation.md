# Installation Guide

## Prerequisites

- Kubernetes 1.24+ or OpenShift 4.x
- Helm 3.x
- Permission to create a Role/RoleBinding in the target namespace (the chart creates them for you)

## Installing via Helm

The chart is published to a Helm repository hosted on GitHub Pages.

1.  Add the repository:
    ```bash
    helm repo add smart-proxy https://ivseb.github.io/smart-proxy
    helm repo update
    ```

2.  Install the chart:
    ```bash
    helm install smart-proxy smart-proxy/smart-proxy \
      --namespace smart-proxy --create-namespace
    ```

3.  Verify the installation:
    ```bash
    kubectl get pods -n smart-proxy
    ```

### Installing from source

Alternatively, install straight from a clone of the repository:

```bash
git clone https://github.com/ivseb/smart-proxy.git
cd smart-proxy
helm install smart-proxy ./charts/smart-proxy --namespace smart-proxy --create-namespace
```

### Customizing the deployment

All options are documented in [`charts/smart-proxy/values.yaml`](../charts/smart-proxy/values.yaml).
A ready-to-edit production example (OpenShift Route + persistence) is provided in
[`charts/smart-proxy/values.example.yaml`](../charts/smart-proxy/values.example.yaml):

```bash
helm install smart-proxy ./charts/smart-proxy \
  --namespace my-namespace --create-namespace \
  -f charts/smart-proxy/values.example.yaml
```

Common overrides:

| Value | Description | Default |
| :--- | :--- | :--- |
| `image.tag` | Image tag to deploy (pin a release in production). | chart `appVersion` |
| `config.logLevel` | `debug`, `info` or `error`. | `info` |
| `rbac.openshiftRoutes` | Grant permissions on OpenShift Routes. Set `false` on vanilla Kubernetes. | `true` |
| `route.enabled` | Expose the admin dashboard via an OpenShift Route. | `false` |
| `ingress.enabled` | Expose the admin dashboard via a Kubernetes Ingress. | `false` |
| `persistence.enabled` | Use a PersistentVolumeClaim instead of an ephemeral `emptyDir`. | `false` |

See the [Configuration Reference](configuration.md) for the full list of environment variables and annotations.

## Local Development (Docker Desktop)

For a full local environment with Smart Proxy and a demo application, use the setup script:

1.  Ensure Docker Desktop is running with Kubernetes enabled.
2.  Run the setup script:
    ```bash
    ./scripts/setup-local.sh
    ```
    This script will:
    - Build the Docker image locally.
    - Deploy Smart Proxy plus a demo Frontend/Backend/Redis application.
    - Configure local Ingress resources.

3.  Access the Dashboard:
    - Add `127.0.0.1 admin.local` to your `/etc/hosts`.
    - Visit `http://admin.local`.

## Docker Image

The image is published on Docker Hub:

```bash
docker pull isebben/smart-proxy:latest
```

<https://hub.docker.com/r/isebben/smart-proxy>
