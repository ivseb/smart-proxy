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
| `IGNORE_DEFAULT_USER_AGENTS` | Ignore the built-in list of uptime monitors and health checkers (matched in the User-Agent). | `true` |
| `IGNORE_USER_AGENTS`, `IGNORE_PATHS`, `IGNORE_SOURCES`, `IGNORE_METHODS` | Extra requests that never count as activity, for every route: User-Agent substrings, paths (`/status/*` for prefixes), client IPs/CIDRs, methods. Comma-separated. | — |
| `TRUSTED_PROXIES` | Proxies whose `X-Forwarded-For` names the client (`none` to trust none). | private networks |
| `WAKE_TIMEOUT` | How long requests other than browser page loads (API calls, form posts, WebSockets) wait for a sleeping application before getting `503`. Page loads get the "waking up" page instead. | `2m` |
| `CLUSTER_DOMAIN` | The cluster's DNS domain, used to reach Services (`<service>.<namespace>.svc.<domain>`). | `cluster.local` |
| `STATS_RETENTION` | How far back the dashboard's traffic charts go. Kept in memory by each replica (a 10-second resolution), so a restart starts over; use the Prometheus metrics for long-term history. | `30m` |
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

An Ingress or Route can only send traffic to a Service of its own namespace. Where Smart Proxy patches one outside its own namespace, it creates a *stand-in*: a Service with Smart Proxy's name and no selector, whose endpoints are Smart Proxy's ready pods, kept up to date as they come and go, and removed when nothing there is patched anymore (or on uninstall). On OpenShift, Endpoints listing pod IPs need the `endpoints/restricted` permission: the chart grants it (installing the chart then needs cluster-admin, as granting a permission requires holding it).

**NetworkPolicies.** Two paths must be open:

- **Ingress controller → Smart Proxy.** The controller sends traffic straight to Smart Proxy's pods, so Smart Proxy's namespace must accept it: on OpenShift, from namespaces labelled `policy-group.network.openshift.io/ingress: ""` (and `policy-group.network.openshift.io/host-network: ""` when the router uses host networking); elsewhere, from your ingress controller's namespace.
- **Smart Proxy → applications.** Requests reach the applications from Smart Proxy's pods. Namespaces whose policies only allow their own namespace and the router (a common OpenShift baseline) must also allow Smart Proxy's namespace, or patched applications time out.

When a namespace stops being managed (removed from `watchNamespaces`, or its label removed), Smart Proxy restores it: its patched Ingresses/Routes point at the applications again and the workloads it put to sleep are woken. Its routes stay in the dashboard as *Unwatched* and are patched again if the namespace comes back. This needs Smart Proxy's permissions in that namespace, which `namespaceSelector` and `allNamespaces` keep; with `watchNamespaces`, the chart removes the Role together with the namespace, so delete its routes first.

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
| `smart_proxy_ignored_requests_total{namespace,route,reason}` | Requests that didn't count as activity (monitors, health checks). |
| `smart_proxy_asleep_responses_total{namespace,route,code}` | Of those, answered by Smart Proxy while the route slept. |
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
| `/__smart_proxy/use/<backend>` | Keeps a browser on one of the route's backends for 12 hours (`default` goes back to the weights). See [Backends](routes.md#backends-send-some-requests-elsewhere). |
| `/__smart_proxy/login`, `/__smart_proxy/logout` | Sign-in page of a [protected route](routes.md#access-require-sign-in-or-a-token). |

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
