# Upgrading

## From 2.2 to 2.3

A drop-in upgrade with the chart (`helm upgrade … --version 0.5.1 -f my-values.yaml`). Nothing changes for existing routes. To know:

- **Smart Proxy's own Secret.** On first start Smart Proxy creates `<release>-state` in its namespace (deleted with it on uninstall): the token replicas use to share recorded requests, the key signing sign-in cookies, and the hashed passwords and tokens of protected routes. The chart grants the permissions (create Secrets; read and update only that one). **Without the chart**, add them to your Role: see `deploy/kubernetes/rbac.yaml`. Without them Smart Proxy works as before, but the inspector shows the requests of one replica only and routes can't be protected (the dashboard says so).
- **New reserved paths** on every host: `/__smart_proxy/use/…`, `/__smart_proxy/login`, `/__smart_proxy/logout` (see [Configuration](configuration.md#reserved-paths)).
- **`X-Smart-Proxy-User`** (in any spelling) and Smart Proxy's `sp_auth_*` cookies are removed from every request before it reaches an application.

## From 2.1 to 2.2

A drop-in upgrade with the chart (`helm upgrade … --version 0.4.0 -f my-values.yaml`). Things to know:

- **Stand-in Services.** In every namespace (other than its own) where Smart Proxy has patched an Ingress or Route, it now creates a Service with its own name and no selector, whose endpoints are its pods. Before, patched resources outside Smart Proxy's namespace pointed at a Service that didn't exist there. The chart grants the new permissions (create Services and Endpoints; change or delete only those with Smart Proxy's name). **Without the chart**, add them to your Roles: see `deploy/kubernetes/rbac.yaml`. If a namespace already has a Service with Smart Proxy's name that isn't Smart Proxy's, patching there fails with a clear error instead of breaking the application.
- **API calls to sleeping applications wait** for them (up to `WAKE_TIMEOUT`, 2 minutes) instead of getting the HTML waking page; browsers still get the page.
- **Self-healing backs off** when something keeps reverting a patch (see [GitOps](gitops.md#argo-cd)).

## From 2.0 to 2.1

A drop-in upgrade (`helm upgrade … --version 0.3.0 -f my-values.yaml`). Things to know:

- **Uptime monitors no longer keep applications awake.** Requests from common monitors (UptimeRobot, Pingdom, StatusCake, Datadog, Blackbox Exporter, kube-probe, …) don't count as activity anymore, and while an application sleeps they get `200` from Smart Proxy. An environment that only monitors were visiting will go to sleep after its idle timeout. To keep the old behaviour, set `ignore.defaultUserAgents: false`, or set a route's "While the app sleeps" to *Wake the app*.
- **Deleting a route restores every Ingress/Route it patched**, and the "delete configuration only" option is gone. Resources left patched by an earlier deletion can be restored from the Patching page.
- **Routes balancing several Services** keep their weighted split once patched. Routes patched with 2.0 still send all traffic to their main Service: on the Patching page, unpatch them and patch them again. Smart Proxy then records their backends and manages only those running at that moment (a backend kept off on purpose stays off); check the result in the route's detail.
- **DeploymentConfigs** are managed on OpenShift; the chart's Role gains `apps.openshift.io/deploymentconfigs` (with `rbac.openshiftRoutes`). Without the chart, add that rule to your Role.

## From 1.x (chart 0.1.x) to 2.0 (chart 0.2.0)

2.0 adds dashboard authentication, high availability, multiple namespaces, safe uninstall, metrics, schedules, StatefulSets and declarative configuration. Most of it works without changes, but a few defaults and internals changed. Read this section before upgrading.

### What changes for you

| Change | Impact | What to do |
| :--- | :--- | :--- |
| **Dashboard authentication is on** (Helm default `auth.mode: basic`) | The dashboard asks for a password after the upgrade. | Read the generated password (see below), or choose another mode: [Authentication](authentication.md). `auth.mode: none` restores the old behaviour. |
| **Two replicas** (`replicaCount: 2`) | Smart Proxy is no longer a single point of failure for patched applications. | Nothing. Set `replicaCount: 1` to keep one replica. |
| **Routes live in a ConfigMap** (`<release>-routes`) | Shared by all replicas; no volume needed. | Patched Ingresses/Routes are picked up automatically from their annotations. **Routes created by hand** (not by patching) were kept inside the 1.x container: export them first (step 1). |
| **The image uses `ENTRYPOINT`** | Running `smart-proxy` is unchanged. | If you override the container `command` to pass flags, pass them as `args` instead. |
| **Patched Ingresses use the Service's named port** (`proxy`) | Fixes Ingresses pointing at a port the chart's Service didn't expose. | Nothing: they are migrated automatically within 30 seconds of the upgrade. |
| **Probes** | Liveness `/__smart_proxy/healthz`, readiness `/__smart_proxy/readyz` on the proxy port. | Only if you deploy without the chart: update your manifest. |
| **RBAC** | New permissions: StatefulSets, HorizontalPodAutoscalers, ConfigMaps and Leases in Smart Proxy's namespace. | Helm does it. Without the chart, re-apply `deploy/kubernetes/rbac.yaml`. |
| **`persistence`** | Deprecated (routes are in the ConfigMap). | Leave it as it is; an existing `/data/routes.json` is imported on the first start. |

### 1. Export manually created routes (if any)

Routes you created with **New Route** (as opposed to patching an Ingress/Route) were stored in the 1.x container and are not migrated. Save them before upgrading:

```bash
kubectl -n smart-proxy exec deploy/smart-proxy -- cat /app/routes.json > routes-1.x.json
```

Patched resources need nothing: their configuration is also stored in their `smart-proxy/config` annotation.

### 2. Upgrade with Helm

```bash
helm repo update
helm upgrade smart-proxy smart-proxy/smart-proxy \
  --namespace smart-proxy \
  --version 0.2.0 \
  -f my-values.yaml
```

Pass your own values file rather than `--reuse-values`: with `--reuse-values`, Helm keeps only the old chart's values and the new settings (`auth`, `metrics`, …) would be missing. On Helm 3.14+ you can use `--reset-then-reuse-values` instead.

Then read the generated dashboard password:

```bash
kubectl -n smart-proxy get secret smart-proxy-auth -o jsonpath='{.data.basic-password}' | base64 -d
```

(The Secret is `<release>-smart-proxy-auth`, or `smart-proxy-auth` when the release is called `smart-proxy`.)

### 3. Re-create manual routes

For each route saved in step 1 that isn't in the dashboard yet:

```bash
jq -c '.[] | select(.id | test("^(ing|route)-") | not)' routes-1.x.json | while read -r route; do
  curl -u admin:<password> -H 'Content-Type: application/json' -d "$route" \
    https://<dashboard-host>/api/routes
done
```

### 4. Check

```bash
kubectl -n smart-proxy get pods                        # 2/2 replicas ready
kubectl -n smart-proxy logs deploy/smart-proxy | grep -i leader
kubectl -n smart-proxy get configmap smart-proxy-routes
```

In the dashboard, every patched route should be listed with its namespace. Within 30 seconds the leader re-applies the patches with the new named port (`Self-Healing Success` in the logs).

### Without Helm

Re-apply the updated manifests from `deploy/kubernetes/` (RBAC first). The new Deployment sets `POD_NAME`, `POD_UID`, `SMART_PROXY_DEPLOYMENT` and the new probes. Without `AUTH_MODE` the dashboard stays unauthenticated, as in 1.x: see [Authentication](authentication.md) to enable it.

### Rolling back

`helm rollback` works, but 1.x does not understand everything 2.0 records (for example namespaced route IDs). To go back cleanly, restore the cluster with 2.0 first, then roll back and patch your resources again from the 1.x dashboard:

```bash
kubectl -n smart-proxy scale deployment/smart-proxy --replicas=0
kubectl -n smart-proxy run smart-proxy-restore --rm -i --restart=Never \
  --image=isebben/smart-proxy:2.0.0 \
  --overrides='{"spec":{"serviceAccountName":"smart-proxy-sa"}}' \
  --env=WATCH_NAMESPACE=smart-proxy \
  -- restore
helm rollback smart-proxy --namespace smart-proxy
```
