# Smart Proxy Helm Chart

Intelligent request handling for Kubernetes Ingresses and OpenShift Routes — auto-sleep,
dependency chains and unified route patching.

## Install

```bash
helm repo add smart-proxy https://ivseb.github.io/smart-proxy
helm repo update
helm install smart-proxy smart-proxy/smart-proxy \
  --namespace smart-proxy --create-namespace
```

Install from source instead:

```bash
helm install smart-proxy ./charts/smart-proxy --namespace smart-proxy --create-namespace
```

## Configuration

Common values (see [`values.yaml`](values.yaml) for the full, commented list and
[`values.example.yaml`](values.example.yaml) for a production example):

| Key | Description | Default |
| :--- | :--- | :--- |
| `image.repository` | Container image. | `docker.io/isebben/smart-proxy` |
| `image.tag` | Image tag (defaults to chart `appVersion`). | `""` |
| `config.logLevel` | `debug`, `info` or `error`. | `info` |
| `config.watchNamespace` | Namespace to watch (empty = release namespace). | `""` |
| `rbac.create` | Create the Role/RoleBinding Smart Proxy needs. | `true` |
| `rbac.openshiftRoutes` | Also grant permissions on OpenShift Routes. | `true` |
| `route.enabled` | Expose the admin dashboard via an OpenShift Route. | `false` |
| `ingress.enabled` | Expose the admin dashboard via a Kubernetes Ingress. | `false` |
| `persistence.enabled` | Use a PVC instead of an ephemeral `emptyDir`. | `false` |

## Uninstall

```bash
helm uninstall smart-proxy --namespace smart-proxy
```
