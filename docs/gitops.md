# GitOps

Smart Proxy changes two things in your cluster: the backend of patched Ingresses/Routes and the replica count of sleeping workloads. GitOps tools that enforce the state in Git would undo both, so tell them to leave these fields alone, and keep Smart Proxy's settings in Git with annotations.

## Configure routes with annotations

Instead of patching from the dashboard, opt an Ingress or Route in with annotations, in the same manifest as your application:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: shop
  annotations:
    smart-proxy/enabled: "true"
    smart-proxy/idle-timeout: 45m
    smart-proxy/dependencies: api, statefulset/postgres:keep
    smart-proxy/start-in-order: "true"
    smart-proxy/schedule: mon-fri 08:00-19:00 Europe/Rome
spec:
  ...
```

| Annotation | Meaning | Default |
| :--- | :--- | :--- |
| `smart-proxy/enabled` | Put this Ingress/Route behind Smart Proxy. Setting it to `false` (or removing it) restores the resource. | — |
| `smart-proxy/idle-timeout` | Sleep after this much inactivity. | `30m` |
| `smart-proxy/dependencies` | Workloads woken with the app, comma-separated (`statefulset/<name>` for StatefulSets). Append `:keep` to leave one running when the app sleeps. | none |
| `smart-proxy/start-in-order` | Wake the dependencies one at a time, in order, before the app. | `false` |
| `smart-proxy/always-on` | Never sleep (traffic still goes through Smart Proxy). | `false` |
| `smart-proxy/schedule` | Keep awake during these hours: `<days> <from>-<to> [timezone]`, e.g. `mon-fri 08:00-19:00 Europe/Rome`, `daily 22:00-06:00`. | none |
| `smart-proxy/workload` | The workload to scale, when it can't be inferred from the Service (`web`, `statefulset/web`). | inferred |
| `smart-proxy/badge` | Show a "Powered by Smart Proxy" badge on HTML pages. | `false` |

Smart Proxy applies changes within 30 seconds. Routes defined this way are marked *annotations* in the dashboard; editing them there is overwritten by the annotations. An invalid value is reported in the logs and the last valid configuration is kept.

## Argo CD

Ignore the fields Smart Proxy manages, and make Argo CD respect that when syncing:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jsonPointers: [/spec/replicas]
    - group: apps
      kind: StatefulSet
      jsonPointers: [/spec/replicas]
    - group: networking.k8s.io
      kind: Ingress
      jqPathExpressions: [.spec.rules[].http.paths[].backend]
    - group: route.openshift.io
      kind: Route
      jsonPointers: [/spec/to, /spec/port, /spec/alternateBackends]
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true
```

Leaving `replicas` out of your Deployment manifests (as you would with an HPA) also avoids the conflict.

## Flux

Flux's kustomize-controller doesn't ignore fields, but it can leave objects alone once created. Annotate the Ingresses/Routes and workloads managed by Smart Proxy with:

```yaml
metadata:
  annotations:
    kustomize.toolkit.fluxcd.io/ssa: IfNotPresent
```

Alternatively omit `replicas` from the workload manifests, and accept that Flux re-applies the Ingress backend on each sync: Smart Proxy patches it again within 30 seconds (self-healing), during which requests reach the application directly.

## Helm-managed applications

`helm upgrade` of an application resets the Ingress backend and the replica count from its chart. Smart Proxy re-applies the patch within 30 seconds; a workload woken by the upgrade goes back to sleep after its idle timeout.
