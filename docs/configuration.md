# Configuration

## Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SMART_PROXY_PORT` | The HTTP port the proxy listens on. | `8080` |
| `SMART_PROXY_SERVICE_NAME` | Name of the Service fronting Smart Proxy. Patched Ingresses/Routes are pointed at it (on its `proxy` port). Set automatically by the Helm chart. | `smart-proxy` |
| `WATCH_NAMESPACE` | The namespace to watch for resources. | `default` (or current NS) |
| `LOG_LEVEL` | Logging verbosity (debug, info, error). | `info` |
| `ADMIN_ADDR` | Listen address of the admin dashboard. | `:8081` |
| `SHUTDOWN_DELAY` | On SIGTERM, how long to fail the readiness probe before closing listeners, so in-flight traffic moves away cleanly. | `5s` |
| `CONFIG_PATH` | File where route configurations are saved. | `routes.json` |
| `AUTH_MODE` | Dashboard authentication: `none`, `basic`, `token`, `oidc` or `header`. See [Authentication](authentication.md) for all `AUTH_*` variables. | `none` |

## Reserved paths

Smart Proxy answers these paths itself on the proxy port, for every host:

| Path | Purpose |
| :--- | :--- |
| `/__smart_proxy/healthz` | Liveness/readiness probe (returns 503 while shutting down). |
| `/__smart_proxy/status` | Wake-up status polled by the "waking up" page. |

## Annotations

Smart Proxy uses annotations on Ingress/Route objects to store state and configuration.

| Annotation | Description |
| :--- | :--- |
| `smart-proxy/patched` | `true` if the resource is currently managed by Smart Proxy. |
| `smart-proxy/original-service` | The name of the backend service before patching. |
| `smart-proxy/original-port` | The port of the backend service before patching. |
| `smart-proxy/config` | JSON string containing advanced configuration (dependencies, timeouts). |
| `smart-proxy/replicas-before-sleep` | Set on a **Deployment** while it sleeps: the replica count restored when it wakes up. |

## Helm Values

See the `charts/smart-proxy/values.yaml` file for a complete list of Helm configuration options.
