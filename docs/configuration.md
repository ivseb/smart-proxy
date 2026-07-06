# Configuration

## Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SMART_PROXY_PORT` | The HTTP port the proxy listens on. | `80` |
| `WATCH_NAMESPACE` | The namespace to watch for resources. | `default` (or current NS) |
| `LOG_LEVEL` | Logging verbosity (debug, info, error). | `info` |

## Annotations

Smart Proxy uses annotations on Ingress/Route objects to store state and configuration.

| Annotation | Description |
| :--- | :--- |
| `smart-proxy/patched` | `true` if the resource is currently managed by Smart Proxy. |
| `smart-proxy/original-service` | The name of the backend service before patching. |
| `smart-proxy/original-port` | The port of the backend service before patching. |
| `smart-proxy/config` | JSON string containing advanced configuration (dependencies, timeouts). |

## Helm Values

See the `charts/smart-proxy/values.yaml` file for a complete list of Helm configuration options.
