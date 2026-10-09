# Upgrading

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
