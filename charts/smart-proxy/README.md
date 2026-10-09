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
| `config.watchNamespaces` | Namespaces to manage (empty = release namespace). | `[]` |
| `config.allNamespaces` | Manage every namespace (creates a ClusterRole). | `false` |
| `config.namespaceSelector` | Manage namespaces with matching labels, e.g. `smart-proxy=enabled` (ClusterRole). | `""` |
| `rbac.create` | Create the Role/RoleBinding Smart Proxy needs. | `true` |
| `rbac.openshiftRoutes` | Also grant permissions on OpenShift Routes. | `true` |
| `route.enabled` | Expose the admin dashboard via an OpenShift Route. | `false` |
| `ingress.enabled` | Expose the admin dashboard via a Kubernetes Ingress. | `false` |
| `auth.mode` | Dashboard sign-in: `basic`, `token`, `oidc`, `openshift`, `header` or `none` ([docs](https://ivseb.github.io/smart-proxy/authentication/)). | `basic` |
| `auth.existingSecret` | Use your own Secret for the credentials (recommended with GitOps). | `""` |
| `replicaCount` | Replicas; they share routes and activity and elect a leader. | `2` |
| `podDisruptionBudget.enabled` | Keep one replica up during drains (when `replicaCount` > 1). | `true` |
| `persistence.enabled` | Deprecated: routes now live in a ConfigMap. | `false` |
| `podSecurityContext` | Pod securityContext; on vanilla Kubernetes with persistence, set `fsGroup: 1001` so the volume is writable. | `{}` |

## Upgrading

See the [upgrade guide](https://ivseb.github.io/smart-proxy/upgrading/). From 0.4.x to 0.5.0: Smart Proxy creates its own Secret, `<release>-state` (the chart grants access to it). From 0.3.x to 0.4.0: patched resources outside Smart Proxy's namespace now reach it through a stand-in Service created there (the chart grants the new permissions; on OpenShift installing needs cluster-admin for `endpoints/restricted`). From 0.2.x to 0.3.0: uptime monitors no longer keep applications awake (`ignore.defaultUserAgents: false` restores the old behaviour).

### From 0.1.x

Version 0.2.0 (Smart Proxy 2.0) enables dashboard authentication by default, runs two replicas and moves routes into a ConfigMap. Follow the [upgrade guide](https://ivseb.github.io/smart-proxy/upgrading/) — in particular, export manually created routes before upgrading, and upgrade with your values file rather than `--reuse-values`.

## Uninstall

```bash
helm uninstall smart-proxy --namespace smart-proxy
```

Uninstalling restores every patched Ingress/Route to its original Service and wakes sleeping Deployments first (`restoreOnUninstall`, on by default), so applications keep working.
