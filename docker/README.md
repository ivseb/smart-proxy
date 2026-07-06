# Smart Proxy

**Scale-to-zero and smart traffic management for Kubernetes & OpenShift — without touching your apps.**

![Smart Proxy demo](https://raw.githubusercontent.com/ivseb/smart-proxy/main/media/SmartProxy.gif)

Smart Proxy sits in front of your services and puts **idle Deployments to sleep** (scaled to zero), then **wakes them on the first incoming request** — showing a friendly "waking up" page in the meantime. It works by patching your existing Kubernetes **Ingress** or OpenShift **Route**, so there are no sidecars and no application changes.

## Why?

- 💤 **Cut costs on idle environments** — preview, dev, staging and demo environments stop consuming CPU/RAM when nobody is using them.
- 🔗 **Dependency chains** — keep a whole set of services (app → API → DB) awake together, and let them sleep together.
- 🔀 **One tool for Ingress *and* Routes** — manage vanilla Kubernetes and OpenShift from a single dashboard.
- 🪶 **Zero app changes** — it's annotation-based and fully reversible.

## Quick start

```bash
docker pull isebben/smart-proxy:latest
```

Deploy on Kubernetes/OpenShift with Helm:

```bash
helm repo add smart-proxy https://ivseb.github.io/smart-proxy
helm install smart-proxy smart-proxy/smart-proxy --namespace smart-proxy --create-namespace
```

## Supported tags

- `latest` — latest build from the `main` branch
- `X.Y.Z` — released versions (e.g. `1.0.0`)

## Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SMART_PROXY_PORT` | HTTP port the proxy listens on. | `80` |
| `WATCH_NAMESPACE` | Namespace to watch for resources. | current namespace |
| `LOG_LEVEL` | `debug`, `info` or `error`. | `info` |

Exposed ports: `8080` (proxy) and `8081` (admin dashboard).

## Links

- 📖 **Documentation:** https://ivseb.github.io/smart-proxy/
- 💻 **Source:** https://github.com/ivseb/smart-proxy
- ⎈ **Helm chart:** https://artifacthub.io/packages/helm/smart-proxy/smart-proxy

## License

[MIT](https://github.com/ivseb/smart-proxy/blob/main/LICENSE)
