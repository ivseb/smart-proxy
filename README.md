<div align="center">

<img src="media/logo.png" alt="Smart Proxy" width="140"/>

# Smart Proxy

### Scale-to-zero and smart traffic management for Kubernetes & OpenShift — *without touching your apps.*

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/smart-proxy)](https://artifacthub.io/packages/helm/smart-proxy/smart-proxy)
[![Docker Pulls](https://img.shields.io/docker/pulls/isebben/smart-proxy)](https://hub.docker.com/r/isebben/smart-proxy)
[![Helm](https://img.shields.io/badge/helm-ivseb.github.io%2Fsmart--proxy-0f1689)](https://ivseb.github.io/smart-proxy)
[![License](https://img.shields.io/github/license/ivseb/smart-proxy)](LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/ivseb/smart-proxy)](https://github.com/ivseb/smart-proxy/releases)
[![Docs](https://img.shields.io/badge/docs-gh--pages-blue)](https://ivseb.github.io/smart-proxy/)

<img src="media/dashboard-routes.png" alt="Smart Proxy dashboard: routes grouped by namespace, with their state and sleep timers" width="820"/>

</div>

---

## Why Smart Proxy?

Idle environments burn money. Preview, dev, staging and demo namespaces sit running 24/7 while nobody looks at them — and most apps can't scale to zero on their own.

**Smart Proxy fixes that.** It sits in front of your services, puts inactive Deployments to **sleep (0 replicas)**, and **wakes them on the first request** — showing a friendly "waking up" page while the pod starts. It does this by patching your *existing* Ingress or Route, so there are **no sidecars, no rewrites, and no code changes**. Flip it off and everything reverts.

## ✨ Highlights

| | |
| :--- | :--- |
| 💤 **Auto-sleep & instant wake** | Scale idle deployments to zero; the next request transparently wakes them back up. |
| 🔗 **Dependency chains** | Keep `app → api → db` awake together, optionally starting them in order, and let them sleep together. |
| 🩺 **Monitor-aware** | Uptime monitors and health checks never keep apps awake or wake them; they get a 200 while apps sleep. See who keeps an app awake and ignore it in one click. |
| 🔍 **Requests inspector** | Record what reaches a route for a few minutes: headers (credentials masked), cookies, and what Smart Proxy did with each request. |
| 🧪 **Try a new version with some users** | Send requests matching a condition (a header, cookie, path, client IP…) to another backend and keep those users there — e.g. the beta of your mobile app on the next version of the API. |
| 🔒 **Built-in sign-in** | Put any app behind a sign-in page or access tokens, without an identity provider and without touching the app. |
| 🗓️ **Schedules** | Keep apps awake during office hours (any timezone) and let them sleep the rest of the time. |
| 📈 **Prometheus metrics** | Cold-start durations, wake-ups, sleeping deployments and replica-hours saved. |
| 🔀 **Ingress *and* Routes** | One dashboard for both vanilla Kubernetes Ingresses and OpenShift Routes. |
| 🗂️ **Many namespaces** | Manage a list of namespaces, all of them, or any namespace you label `smart-proxy=enabled`. |
| 🎛️ **Admin dashboard** | Routes grouped by namespace with search and filters, live status and "sleeps in" timers, one-click patching, wake and sleep. |
| 🔐 **Secure by default** | Dashboard sign-in with basic auth, tokens, SSO via OIDC (Keycloak, Entra ID, Google…) or OpenShift login. |
| 🛡️ **Built for production** | Two replicas with leader election, WebSockets and streams, gRPC, bursts of requests sharing one wake-up, GitOps-aware self-healing — tested end to end on a real cluster. |
| 🪶 **Zero app changes** | Fully annotation-based and reversible — nothing to add to your images. Opt in from the dashboard or with annotations in Git. |

## 🖥️ The dashboard

Traffic of the last 30 minutes and what sleeps, at a glance:

<img src="media/dashboard-overview.png" alt="Overview: deployments asleep, requests per second over the last 30 minutes, namespaces" width="820"/>

Patch Ingresses and Routes in one click, across namespaces:

<img src="media/dashboard-patching.png" alt="Patching view listing Ingresses across namespaces, with their Service and Deployment state" width="820"/>

The dashboard, the "waking up" page and the sign-in page speak **English and Italian**, following the browser's language (the dashboard has a switch in its header).

## 🧰 More than sleeping

Three optional tools on each route's page — routes that don't use them work as before. [Read more →](docs/routes.md)

**See what arrives.** Record a route's requests for a few minutes: who sends them, their headers (credentials masked), and what Smart Proxy did with each — answered by the app, woke it first, showed the waking page, which backend got it and why.

<img src="media/route-requests.png" alt="Requests recorded for a route, one expanded with its headers, showing a request of the app's beta sent to the next version" width="820"/>

**Try a new version with some users.** Give a route a second backend and the conditions that send requests to it — here, the beta of the mobile app, recognized by its `X-App-Version` header. Those users stay on the new version for the rest of their session; everyone else keeps the current one. **Route like this…** on a recorded request turns one of its headers into a condition.

<img src="media/route-backends.png" alt="A route's backends: the current version takes all traffic, the next one only the mobile app's beta and the testers" width="820"/>

**Require sign-in or a token.** Put an application behind a sign-in page served on its own address (a shared password, or named people) and access tokens for scripts — no identity provider needed. The application gets the caller in `X-Smart-Proxy-User`; strangers never wake it.

<p>
  <img src="media/route-access.png" alt="Access settings of a route: people who can sign in, access tokens, paths open without signing in" width="560"/>
  <img src="media/sign-in.png" alt="Sign-in page served by Smart Proxy on the application's address" width="250"/>
</p>

## 🎬 How it works

1. **Patch** a route from the dashboard → Smart Proxy points the Ingress/Route to itself (originals saved in annotations).
2. **Serve** → incoming traffic hits Smart Proxy, which checks the target's state.
3. **Wake** → if the deployment is asleep, it scales it up: browsers see a "waking up" page until it's ready, API calls and WebSockets simply wait for it.
4. **Sleep** → after an idle timeout with no traffic, it scales the deployment back to zero.

<p align="center">
  <img src="media/waking-page.png" alt="The waking up page a browser sees while a sleeping application starts" width="420"/>
</p>

See the [Architecture overview](docs/architecture.md) for the details.

## 🚀 Quick start

**On Kubernetes / OpenShift (Helm):**

```bash
helm repo add smart-proxy https://ivseb.github.io/smart-proxy
helm install smart-proxy smart-proxy/smart-proxy --namespace smart-proxy --create-namespace
```

The dashboard is protected with a generated password by default (user `admin`):

```bash
kubectl get secret -n smart-proxy smart-proxy-auth -o jsonpath='{.data.basic-password}' | base64 -d
```

Prefer single sign-on? Switch to OIDC or OpenShift login, see [Authentication](docs/authentication.md).

**Try it locally (Docker Desktop + Kubernetes):**

```bash
./scripts/setup-local.sh
```

Then open the dashboard at [http://admin.local](http://admin.local) *(add `127.0.0.1 admin.local` to your `/etc/hosts`)*.

## 📦 Great for

- **Preview / PR environments** that spin up per branch and sit idle most of the day
- **Dev & staging** clusters where cost matters more than always-on latency
- **Internal tools & dashboards** used a few times a week
- **Demo environments** that should wake up the moment someone visits

## 📚 Documentation

- [Installation Guide](docs/installation.md) — Helm, from source, and all values
- [Architecture Overview](docs/architecture.md) — how patching, waking and dependencies work
- [Configuration Reference](docs/configuration.md) — environment variables and annotations
- [Upgrading](docs/upgrading.md) — what changes from one version to the next
- [Changelog](CHANGELOG.md)
- [Inspect, route, protect](docs/routes.md) — record what reaches a route, send some requests to another backend (e.g. a beta), require sign-in or an access token
- [GitOps](docs/gitops.md) — configure routes with annotations; Argo CD and Flux settings
- [Authentication](docs/authentication.md) — securing the dashboard with basic, token, OIDC/SSO or OpenShift login

## 🤝 Contributing

Issues and pull requests are welcome. If Smart Proxy saves you some cluster bills, a ⭐ on GitHub is appreciated!

`go test -race ./...` runs the unit tests. `test/e2e/run.sh` runs the end-to-end tests on a local [kind](https://kind.sigs.k8s.io) cluster (Docker, kind, kubectl and Helm needed): through ingress-nginx, waking from a browser, API calls with large bodies, bursts of requests, WebSockets, gRPC-style HTTP/2, the requests inspector, backend conditions, sign-in and tokens, long streams, leader failover and uninstall. CI runs both.

## License

Released under the [MIT License](LICENSE).
