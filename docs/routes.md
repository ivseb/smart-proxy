# Inspecting, routing and protecting routes

Three optional tools on each route's page in the dashboard. A route that doesn't use them works exactly as before.

## Requests: see what arrives

**Record** on a route's page records its requests for 5, 15 or 60 minutes, then stops by itself. Each request shows:

- what was sent: method, path, query parameter names, headers (credentials such as `Authorization`, cookies, tokens and API keys are masked), cookie names, client IP and user agent. Bodies are never recorded.
- what Smart Proxy did with it: answered by the application, woke it up first, showed the waking page, answered for it while asleep (an ignored monitor), refused it (no login), or couldn't reach it; which backend got it and why.

Every replica records the requests it serves; the dashboard gathers them, whichever replica it talks to. Recordings are kept in memory (the latest 300 per route and replica).

Use it to find what identifies a client before writing an [ignore rule](configuration.md) or a backend condition: from a recorded request, **Route like this…** next to a header turns it into a condition.

## Backends: send some requests elsewhere

A route can have more than one backend, each a Service of its namespace. **Add a backend** on the route's page.

- **Conditions** send matching requests to a backend, whatever the weights: a header, cookie or query parameter that *is*, *contains*, *starts with* a value or *is present*; a path; a client IP or CIDR. Any of a backend's conditions is enough; the first backend with a matching condition wins.
- **Weights** share the other requests among the backends that are running. A backend with weight `0` gets only the requests matching its conditions.
- **The browser stays** on the backend it was sent to (cookie), so a login or session started there continues there. Over HTTPS the cookie is `SameSite=None`, so it also comes with cross-site form posts, such as an identity provider's SAML response.
- **Managed** backends are woken by the requests sent to them and sleep with the route; others are never touched. A request sent by a condition to an unmanaged backend that isn't running gets `503` (never another backend).
- **The link** `https://<host>/__smart_proxy/use/<backend>` keeps a browser on a backend for 12 hours; `…/use/default` goes back to the weights.

### Example: trying a new SAML identity provider in production

Users reach your application through an external portal and sign in with SAML. You want to try a new identity provider with some users, without touching the others.

1. Deploy the version configured for the new provider next to the current one, in the same namespace (e.g. `portal-b`).
2. On the route's page, **Add a backend** → `portal-b` (weight 0, conditions only).
3. Find what identifies the new provider: start recording, sign in through it once, and look at the request reaching your ACS endpoint. Its `Origin` header is usually the provider's address. **Route like this…** on that header adds the condition.
4. From then on, the new provider's responses go to `portal-b`, and so does everything those users do next. Everyone else stays on the current version.

If the provider doesn't send `Origin`, testers open `https://<host>/__smart_proxy/use/portal-b` once before signing in.

## Access: require sign-in or a token

**Require sign-in** on a route's page puts the application behind:

- **People**, who sign in on a page served by Smart Proxy on the application's own address (`/__smart_proxy/login`, `/__smart_proxy/logout`). With a single person the page only asks for the password: use it as a shared password for a team. A login lasts 12 hours. Removing a person signs them out.
- **Access tokens**, for scripts and other services: `Authorization: Bearer <token>`, or `X-Api-Key: <token>` when the application uses `Authorization` itself. Each token is named after who uses it, shown once, and can be revoked.
- **Open paths**, reachable without signing in: an identity provider's callback (`/saml/acs`), webhooks, health checks (`/health*`).

The application receives the caller in the `X-Smart-Proxy-User` header (a person's name, or `token:<name>`); Smart Proxy's own credentials and cookie are removed. Requests without credentials never wake the application and don't count as activity.

Passwords (PBKDF2-SHA256) and tokens (SHA-256) are stored hashed in Smart Proxy's own Secret, `<release>-state`. Failed sign-ins (on the page or with Basic credentials) are limited to 10 per client and 30 per person every 5 minutes. Changing a password signs out everyone who used the old one. Paths are checked the way applications read them (`;` parameters, `..`, backslashes), so an open path can't be used to reach a protected one.

!!! warning "What it doesn't protect"
    Only traffic going through Smart Proxy is checked. If the route is unpatched or Smart Proxy is uninstalled, the application is reachable without signing in again. Inside the cluster, its Service stays reachable directly: use a NetworkPolicy if that matters.

Routes defined by annotations ([GitOps](gitops.md)) can't be protected or given backends from the dashboard yet.
