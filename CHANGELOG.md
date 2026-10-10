# Changelog

## 2.3.1 — chart 0.5.1

### Added
- **English and Italian**: the dashboard, the "waking up" page and the sign-in pages follow the browser's language; the dashboard has a switch in its header.

### Fixed
- The "waking up" page loaded its styles from a CDN: without internet access (air-gapped clusters, strict egress) it showed unstyled. It is now self-contained, built into the binary.
- The traffic chart spans its whole period (30 minutes by default) with a readable time axis, instead of stretching the first minutes of data.

### Changed
- Examples in the dashboard and docs no longer revolve around SAML: backends with conditions are shown with a beta of a mobile app, open paths with webhooks.

## 2.3.0 — chart 0.5.0

Three optional tools on each route's page; routes not using them work as before. Upgrading from 2.2: see [the upgrade notes](docs/upgrading.md#from-22-to-23).

### Added
- **Requests inspector**: record a route's requests for a few minutes and see what was sent (headers with credentials masked, cookie and parameter names) and what Smart Proxy did with each. Gathered from every replica. See [Inspect, route, protect](docs/routes.md).
- **Backends with conditions**: any route can get more backends (Services of its namespace). Requests matching a backend's conditions (header, cookie, query parameter, path, client IP) go there whatever the weights, and the browser stays there, cross-site posts included. `/__smart_proxy/use/<backend>` pins a browser for testing.
- **Built-in protection**: require sign-in (a page on the application's own address, shared password or named people) or access tokens for scripts. The application gets the caller in `X-Smart-Proxy-User`.
- Smart Proxy keeps its own Secret, `<release>-state` (peer token, login key, hashed credentials), created on first start; the chart grants access to it.

## 2.2.0 — chart 0.4.0

A robustness release: two rounds of review plus an end-to-end suite on a real cluster (`test/e2e`, run in CI through ingress-nginx) found and fixed the issues below. Upgrading from 2.1: see [the upgrade notes](docs/upgrading.md#from-21-to-22).

### Fixed — critical
- **Applications in other namespaces than Smart Proxy's were unreachable once patched.** An Ingress or Route can only point at a Service of its own namespace, and Smart Proxy's Service only existed in its own. Smart Proxy now keeps a *stand-in* Service there (same name, no selector, endpoints = Smart Proxy's ready pods, updated as they change) wherever it patches something, and removes it when no longer needed or on uninstall.
- **With two replicas, an app woken by the replica that isn't the leader could be put back to sleep at once**, before that request's activity reached the leader. Wakes are now recorded on the workload (`smart-proxy/woken-at`) and respected.
- **Requests other than page loads got the HTML "waking up" page with `200`**: a webhook or API `POST` to a sleeping app was lost while its sender saw a success. They now wait for the app (`WAKE_TIMEOUT`, 2 minutes) and go through, body included, or get `503` with `Retry-After`. WebSockets too.
- **A readiness probe on `/` stopped the home page from ever waking the app.**

### Fixed
- Open WebSockets, streams and long downloads keep their route active.
- Routes are matched host first (exact, wildcard, then routes without a host), then by longest path, on whole path segments and on the cleaned path; trailing dots in hosts are accepted.
- A burst of requests to a sleeping app makes one wake-up, not one API call each; refused wake-ups (e.g. by an admission webhook) are not proxied to and not retried on every request.
- HTTP/2 without TLS is accepted; gRPC reaches applications as h2c.
- Workloads shared by several routes: an Always On or manual route keeps them up; `db`, `deployment/db` and `sts/x` spellings are one workload.
- Self-healing only touches the route's own resources (another path of the same host is another application), follows applications re-deployed with another Service, rebuilds Routes that lost their annotations, and backs off from GitOps tools that keep reverting it (3 times in 10 minutes: left alone for an hour, with a log line). A route whose resource doesn't point at Smart Proxy is never put to sleep.
- A namespace that stops being managed is restored; its routes are patched again if it comes back. Routes of namespaces Smart Proxy can no longer reach can be deleted.
- Several Smart Proxy installations in one cluster leave each other's patches alone (healing, restore, uninstall).
- Shared state: a route created through another replica no longer looks idle to the leader; the routes ConfigMap survives deletion and never goes back to an older version; traffic statistics can't fill it.
- Unpatching an Ingress/Route re-deployed since (e.g. by Helm) keeps its new spec.
- Saving a route happens before patching its resource; patch errors are reported.
- Restore wakes workloads before unpatching; manual scale-ups of sleeping workloads are no longer woken by restore later; scheduled wakes respect start-in-order.
- The badge leaves `HEAD`, `204`, `206` and `304` responses alone; backend weights are capped at 256; error logs are rate-limited.
- Paths follow the routers: OpenShift Routes match whole segments like Ingresses, Ingress `Exact` paths serve only themselves, plain-prefix controllers (Traefik, `ImplementationSpecific`) still reach the longest prefix; wildcard Routes (`wildcardPolicy: Subdomain`) serve their whole domain.
- Passthrough and re-encrypt Routes are refused (Smart Proxy serves plain HTTP) instead of breaking when patched.

### Added
- Traffic charts keep the last 30 minutes (`STATS_RETENTION`), on the overview and per route.
- `WAKE_TIMEOUT`, `CLUSTER_DOMAIN`.

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
