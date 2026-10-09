# Changelog

## 2.1.0 — chart 0.3.0

Upgrading from 2.0: see [the upgrade notes](docs/upgrading.md#from-20-to-21). Environments kept awake only by uptime monitors will now go to sleep.

### Added
- **Routes balancing several Services** (`alternateBackends`): Smart Proxy keeps the weighted split across the backends that are running (sticky per client), and only wakes and sleeps the *managed* ones; a backend kept off on purpose is never woken. While managed backends wake up, a running unmanaged one answers right away. Routes are suggested for alternate backends too, with their share.
- **OpenShift DeploymentConfigs** as targets and dependencies (`deploymentconfig/<name>` or `dc/<name>`), when the cluster serves `apps.openshift.io`. Previously routes in front of them showed an error and never slept.
- **Uptime monitors and health checks** no longer keep applications awake or wake them: they are recognized by User-Agent (built-in list of common monitors), path, client IP/CIDR or method, globally (`ignore.*`) or per route (dashboard, `smart-proxy/ignore-*` annotations). While an application sleeps they get `200` from Smart Proxy (or `503`, or the app is woken: `when_asleep`).
- **Who keeps it awake**: the route detail lists the clients that sent requests in the last 24 hours, how often, and whether they count, with one-click ignore. New metrics `smart_proxy_ignored_requests_total` and `smart_proxy_asleep_responses_total`.

### Fixed
- Patching a Route with alternate backends sent all traffic to its main Service and dropped the split.
- A Route's numeric `targetPort` (a container port) was dialed as a Service port, and unpatching turned a named `targetPort` into the Service port number; the original port spec is now recorded and restored exactly.
- Deleting a route serving several hosts restored only one of the Ingresses/Routes it had patched; the others kept pointing at Smart Proxy and answered 404. Deleting a route now restores all of them, removing a host from a route restores its resource, and unpatching a resource from the patching page updates its route instead of being re-patched 30 seconds later. The "delete configuration only" option is gone.
- The proxy no longer logs every request (uptime monitors flooded the log view).

## 2.0.0 — chart 0.2.0

Upgrading from 1.x? Read the [upgrade guide](docs/upgrading.md) first: authentication is now on by default, Smart Proxy runs two replicas, and manually created routes must be exported.

### Added
- **Dashboard authentication**, chosen at install time: basic (default), token, OpenID Connect (Keycloak, Entra ID, Google, Okta, Dex…), OpenShift login (oauth-proxy sidecar) or a trusted header from your own auth proxy. CSRF protection for the API.
- **Multiple namespaces**: a list, all namespaces, or every namespace carrying a label (`namespaceSelector`), onboarded without a restart.
- **High availability**: two replicas by default sharing routes (ConfigMap) and activity, with leader election and a PodDisruptionBudget.
- **Safe uninstall**: `helm uninstall` restores every patched Ingress/Route and wakes sleeping workloads first. `smart-proxy restore` does it by hand.
- **Wake at the original size**: the replica count is recorded before sleeping; HPAs wake at `minReplicas`; KEDA-managed workloads are never put to sleep.
- **StatefulSets** as targets and dependencies (`statefulset/<name>`).
- **Ordered start** of dependencies ("Start in order").
- **Schedules**: keep a route awake during given hours and days, in any timezone.
- **Declarative routes** from `smart-proxy/*` annotations on Ingresses/Routes, for GitOps; documentation for Argo CD and Flux.
- **Prometheus metrics** on a dedicated port: cold-start durations, wake-ups, sleeps, sleeping workloads and replicas; optional ServiceMonitor.
- **Dashboard**: routes grouped by namespace with search and filters, "sleeps in" timers, wake/sleep actions, namespace-aware patching, schedule editor, linkable tabs.
- Liveness and readiness probes, graceful shutdown, signed container images (cosign keyless), CI with tests and linting.

### Changed
- Reads are served from informer caches: proxied requests no longer call the Kubernetes API.
- Patched Ingresses point at the Smart Proxy Service's named port `proxy`, and at the release's Service name (it was hardcoded to `smart-proxy`). Existing patches are migrated automatically.
- Route IDs include the namespace (`ing-<namespace>/<name>`); old IDs keep working.
- The image uses `ENTRYPOINT`; `SMART_PROXY_PORT` is the proxy's listen port.
- Kubernetes libraries 0.36, OpenShift client release-4.23, Node 22 for the dashboard build.

### Fixed
- Restarts no longer put every application to sleep on the first watcher tick.
- Unpatching an Ingress restored port 80 instead of the original port; named ports were lost.
- Ingresses with a non-Service backend crashed patching.
- Offline mode (no cluster) crashed the process.
- A route without idle timeout was put to sleep every 30 seconds.
- The dashboard called a non-existent unpatch endpoint, reported failures as successes, showed made-up data, and stopped streaming logs after a reconnect.
- The waiting page waited for every replica instead of the first ready one.

## 1.0.0

Initial public release.
