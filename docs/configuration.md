# Configuration

## Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SMART_PROXY_PORT` | The HTTP port the proxy listens on. | `8080` |
| `SMART_PROXY_SERVICE_NAME` | Name of the Service fronting Smart Proxy. Patched Ingresses/Routes are pointed at it (on its `proxy` port). Set automatically by the Helm chart. | `smart-proxy` |
| `WATCH_NAMESPACE` | Namespaces to manage: one, a comma-separated list, or `*` for all. See [Multiple namespaces](#multiple-namespaces). | its own namespace |
| `WATCH_NAMESPACE_SELECTOR` | Manage every namespace whose labels match this selector (e.g. `smart-proxy=enabled`). | — |
| `POD_NAMESPACE` | The namespace Smart Proxy runs in (set by the chart; otherwise read from the service account). | auto |
| `LOG_LEVEL` | Logging verbosity (debug, info, error). | `info` |
| `ADMIN_ADDR` | Listen address of the admin dashboard. | `:8081` |
| `METRICS_ADDR` | Listen address of the Prometheus metrics endpoint. | `:9090` |
| `SHUTDOWN_DELAY` | On SIGTERM, how long to fail the readiness probe before closing listeners, so in-flight traffic moves away cleanly. | `5s` |
| `CONFIG_PATH` | File where route configurations are saved when running outside a cluster; inside one they live in the `<service>-routes` ConfigMap (an existing file is imported once). | `routes.json` |
| `POD_NAME`, `POD_UID` | This replica's identity, for leader election and its activity ConfigMap (set by the chart). | hostname |
| `SMART_PROXY_DEPLOYMENT` | Smart Proxy's own Deployment: owns the routes ConfigMap, and is stopped by `restore`. | — |
| `AUTH_MODE` | Dashboard authentication: `none`, `basic`, `token`, `oidc` or `header`. See [Authentication](authentication.md) for all `AUTH_*` variables. | `none` |

## Multiple namespaces

One Smart Proxy can manage the Ingresses, Routes and Deployments of many namespaces. Choose one of these Helm settings:

```yaml
config:
  # Only these namespaces (a Role is created in each):
  watchNamespaces: [team-a, team-b, previews]

  # Every namespace (ClusterRole):
  allNamespaces: true

  # Every namespace carrying a label (ClusterRole). New namespaces are picked up as soon as
  # they are labelled, without reinstalling:
  namespaceSelector: smart-proxy=enabled
```

```bash
kubectl label namespace pr-1234 smart-proxy=enabled
```

Smart Proxy keeps a local cache of the managed namespaces (Deployments, Services, Ingresses, Routes and HPAs), so proxied requests never wait on the Kubernetes API. Patching, waking and sleeping happen only inside managed namespaces; the API refuses anything else.

## Metrics

Prometheus metrics are served on a separate port (`METRICS_ADDR`, default `:9090`, path `/metrics`), never through the proxy or behind the dashboard login. The chart exposes it on the Service and can create a ServiceMonitor (`metrics.serviceMonitor.enabled`).

| Metric | Description |
| :--- | :--- |
| `smart_proxy_requests_total{namespace,route}` | Requests proxied to applications. |
| `smart_proxy_wakeups_total{namespace,deployment,trigger}` | Deployments scaled up from zero (`request` or `manual`). |
| `smart_proxy_wake_duration_seconds{namespace,deployment}` | Cold start: from scaling up until a replica is ready. |
| `smart_proxy_sleeps_total{namespace,deployment,reason}` | Deployments scaled to zero (`idle` or `manual`). |
| `smart_proxy_sleeping_deployments{namespace}` | Deployments asleep right now. |
| `smart_proxy_sleeping_replicas{namespace}` | Replicas those deployments would otherwise run. |
| `smart_proxy_leader` | 1 on the replica that sleeps deployments and heals patches. |

Every replica reports the cluster-wide gauges; use `max` over replicas for them, and `sum` for the counters. Useful queries:

```promql
# Replica-hours saved over the last 7 days
sum(sum_over_time((max by (namespace) (smart_proxy_sleeping_replicas))[7d:1m])) / 60

# 95th percentile cold start per deployment
histogram_quantile(0.95, sum by (le, namespace, deployment) (rate(smart_proxy_wake_duration_seconds_bucket[1h])))
```

## Reserved paths

Smart Proxy answers these paths itself on the proxy port, for every host:

| Path | Purpose |
| :--- | :--- |
| `/__smart_proxy/healthz` | Liveness probe (the process is up). |
| `/__smart_proxy/readyz` | Readiness probe: 503 until the Kubernetes caches are synced, and while shutting down. |
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
