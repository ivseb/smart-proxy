# Changelog

## 2.8.6 — chart 0.10.6

### Fixed
- **Start it now** led back to the blocked-hours page instead of the waking page whenever the request after it reached a replica whose cache didn't show the start yet (or the start had failed). The browser that pressed it now gets the waking page for the next 2 minutes, which follows the start and shows a workload that couldn't be started.

## 2.8.5 — chart 0.10.5

Dependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:

- Bump the dashboard group in /web/dashboard with 6 updates

## 2.8.4 — chart 0.10.4

### Security
- Built with Go 1.27.3: fixes [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) in `crypto/x509` (compromised intermediate certificates are distrusted).

### Fixed
- **A waking page left open no longer keeps restarting the application.** The page polls Smart Proxy every second, and each poll woke the application: one that never got ready, scaled down by hand or paused, was scaled back up within a second for as long as some browser kept the page open. A paused route now wakes nothing from there either: the page follows a start made with **Wake now**, or reloads into the 503. Requests waiting for a wake-up stop too when the route is paused.

### Changed
- **The idle timeout starts once the application is ready, not when it was woken.** A stack slower to start than its idle timeout was put back to sleep halfway. Now Smart Proxy leaves a woken stack alone until every dependency and workload has a ready pod, then starts its idle timeout; one still not ready 30 minutes after its wake-up (crash loop, missing image) sleeps when idle, as before. The dashboard shows no sleep time while the application starts.

## 2.8.3 — chart 0.10.3

Dependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:

- Bump github.com/go-jose/go-jose/v4

## 2.8.2 — chart 0.10.2

### Changed
- **A pause leaves the application as it is.** Pausing a route no longer wakes its application: Smart Proxy neither starts nor stops anything while the pause lasts, and only forwards requests. A sleeping application stays asleep — requests get 503 (`X-Smart-Proxy: paused`) until someone starts it with **Wake now**; a running one is never put to sleep.

## 2.8.1 — chart 0.10.1

### Fixed
- The blocked-hours page with **Start it now** is answered with status 200 (was 503), like the waking page: corporate proxies and CDNs replaced it with their own error page. API calls and monitors still get 503; the page still carries `X-Smart-Proxy: blocked`.
- `smart_proxy_sleeps_total` counts the applications put to sleep when blocked hours start with `reason="blocked"` (was `idle`).

## 2.8.0 — chart 0.10.0

### Added
- **Blocked hours.** Hours when requests don't wake sleeping applications (they get 503 with `Retry-After`), e.g. weeknights 22:00–06:00 and the whole weekend; applications are put to sleep when they start, without waiting for their idle timeout. Set globally and per namespace (new **Settings** tab) or per route (its page), with a preview of the week ahead. **Holidays** follow Sunday's rules (one click adds Italy's). **Exceptions** lift them for a while from a route's page: *can be woken* or *kept on*. Always On routes are never blocked. Optionally, browsers get a page saying the application is off outside its hours, with a **Start it now** button and a message of your choice; API calls and monitors still get 503. See [Blocked hours](docs/routes.md#blocked-hours-no-wake-ups-at-night-or-on-weekends).

- **Uptime over the last 12 months, and the savings.** The Report tab gains a *12 months* view, month by month, from a day-by-day history kept for a year (ConfigMap `<release>-uptime-year`; the days already recorded are carried over). Every period now shows the **replica-hours saved**: those each workload would have used always on, minus those it used. CSV included.

### Changed
- **OpenShift sign-in is built in: no more oauth-proxy sidecar.** With `auth.mode: openshift`, Smart Proxy signs users in with their OpenShift account itself: its ServiceAccount is the OAuth client (as before), and who gets in is checked with a `SelfSubjectAccessReview` made with the user's own token — no cluster-wide permission. The pod has a single container: the sidecar's image, with its own vulnerabilities, is gone. `auth.openshift.sar` keeps its format and default (update Services in the release namespace); users refused see which permission they lack. New: `auth.openshift.oauthURL` and `auth.openshift.caConfigMap` for clusters where the OAuth server can't be discovered or its certificate isn't trusted. `auth.openshift.image` and `auth.openshift.resources` are ignored.

### Removed
- framer-motion from the dashboard's dependencies (unused).

## 2.7.2 — chart 0.9.2

Dependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:

- Bump the kubernetes group with 2 updates

## 2.7.1 — chart 0.9.1

Dependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:

- Bump the dashboard group in /web/dashboard with 9 updates
- Bump docker/setup-qemu-action from 3 to 4
- Bump docker/login-action from 3 to 4
- Bump docker/build-push-action from 6 to 7
- Bump actions/setup-go from 5 to 7
- Bump azure/setup-helm from 4 to 5

## 2.7.0 — chart 0.9.0

### Added
- **Uptime, in detail.** Any period of the last 30 days: the last 24 hours, 7 or 30 days, or from… to… down to the hour — on a route's page and in the Report tab.
  - **To the minute**: Smart Proxy now records the minute a workload's replicas change. Periods up to 3 days show a timeline and the list of changes (*woken at 03:12, 2 replicas — put to sleep at 03:41*), with the state at the start of the period: whether an application stayed asleep all night is answered exactly.
  - **Map of the hours**: longer periods show every hour, a column per day, midnight at the top; a night asleep is a dark band. Picking a day opens its timeline and changes.
  - In the Report tab, picking a workload opens its detail; the CSV of a short period has the minutes up of each hour.
  - Restarts of Smart Proxy show as hatched (no data) instead of guessed.
- `/api/uptime` takes `from`/`to`, `workload`, `hours` and `changes`.

## 2.6.0 — chart 0.8.0

### Added
- **Uptime over the last 30 days.** Smart Proxy records every minute whether each workload it manages (the routes' workloads, their backends and dependencies) has at least one replica. A route's page shows its workloads over the last 7 or 30 days, day by day: hours up and down, the share of time up, replica-hours and wake-ups. The new **Report** tab gathers every managed workload by namespace, with totals, sorting, a filter and a CSV download. Kept in the ConfigMap `<release>-uptime`; no new permission. See [Uptime](docs/routes.md#uptime-the-last-30-days).

### Changed
- CI: a newer push cancels the run in progress; the end-to-end tests run only when what they exercise changes (Go code, chart, Dockerfile, modules, the tests), and take a couple of minutes less.

## 2.5.2 — chart 0.7.2

Dependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:

- Bump golang from 1.26.9-alpine to 1.27.2-alpine
- Bump the go-modules group across 1 directory with 2 updates
- Bump node from 22-alpine to 26-alpine
- Bump actions/setup-node from 4 to 7
- Bump docker/setup-buildx-action from 3 to 4
- Bump docker/metadata-action from 5 to 6
- Bump actions/setup-python from 5 to 7
- Bump actions/checkout from 4 to 7

## 2.5.1 — chart 0.7.1

### Security
- The dashboard's packages are updated: the 32 advisories GitHub reported on its build tools (vite, rollup, postcss, esbuild, eslint's dependencies) are fixed. They weren't in the image (Trivy: 0), now they aren't in the repository either.
- **Fixes are released automatically**: Dependabot (alerts and security updates on) proposes updates of the Go modules and the Go version daily; a daily workflow merges those whose checks all passed — tests, end-to-end on kind, Trivy — except major version changes, then tests the result again and publishes a patch release. See [the installation guide](docs/installation.md#whats-in-it-and-vulnerabilities).
- CI tests with the Go version the image is built with.

## 2.5.0 — chart 0.7.0

### Security
- **The image has no known vulnerability** (Trivy: 0, any severity). It is now built `FROM scratch`: the static binary, the dashboard and the CA certificates only — no operating system, shell or package manager. Go 1.26.9 (pinned in `go.mod` and the Dockerfile), `golang.org/x/net` 0.61.0 (HTTP/2 denial-of-service fixes), `github.com/klauspost/compress` 1.20.3 and the other `golang.org/x` modules updated.
- CI now fails on any vulnerability: Trivy scans the image before it is published, `govulncheck` the code. A weekly workflow scans the published image again; Dependabot proposes dependency updates.
- **No shell in the image**: to troubleshoot, attach a debug container (`oc debug`, `kubectl debug`) — see [the installation guide](docs/installation.md#whats-in-it-and-vulnerabilities).

### Added
- **Copy what is cut short**: hosts, paths and user agents shortened in the dashboard show in full on hover and have a copy button. Copying also works when the dashboard isn't served over HTTPS.
- Recorded requests show the **full URL**, ready to copy, with their query parameter values (credentials masked: `access_token`, `api_key`, `code`, `sig`…). Text in the list can be selected without opening the request.

## 2.4.2 — chart 0.6.2

### Added
- **Select all / Deselect all** for the hosts already serving a route's Service, in the route form, with how many are selected.

### Fixed
- Editing a route didn't show the hosts already serving its Service (only creating one did), so hosts couldn't be added or removed by ticking them.

## 2.4.1 — chart 0.6.1

### Added
- **Ignore rules with several conditions**: a rule can combine a path, methods, a user agent and a client IP; a request must meet all of them (any of the methods). E.g. *path `/api/status` and methods GET, HEAD*. Rules with one condition are still stored as before (`ignore.paths`, …), combined ones in `ignore.rules`.

### Fixed
- The README showed the application's page instead of the "waking up" page.

## 2.4.0 — chart 0.6.0

### Added
- **Pause a route**: for 1 hour, 4 hours, until tomorrow morning, until a date, or until resumed. Smart Proxy then only forwards its requests to the application (woken at once, never put to sleep, no other backends); sign-in still applies. When the pause ends the idle timeout starts from there.
- **Ignored requests as a list**: a route's ignore rules are listed one per line, to add, edit and remove, each with the requests it matched in the last 24 hours (across replicas) and when it last did; the global rules and the workloads' probe paths are shown too. A recorded request can be turned into a rule (its path, user agent or IP).
- **Export and import routes**: download some or all routes as a file; importing shows first what each route would do (new, changed settings, unchanged, can't import) and imports the ones you pick.
- **Recently deleted**: deleted routes are kept for 30 days, people and tokens included, and restored in one click (ConfigMap `<release>-deleted-routes`).
- **Requests inspector**: search with several words and `-word` to exclude, quick views (without ignored, problems, ignored only, one backend), a scrolling list with a fixed header.

### Fixed
- In the requests inspector, an open request closed by itself when new requests arrived; the list now holds still while one is open, and counts the new ones.
- Saving a route from a page opened earlier could bring back a recording that had been stopped meanwhile.

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
