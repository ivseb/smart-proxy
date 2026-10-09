# Authentication

The admin dashboard can patch Ingresses/Routes and scale Deployments, so it should never be reachable without authentication. Smart Proxy supports several modes; whoever installs it picks the one that fits their cluster with `auth.mode` (Helm) or `AUTH_MODE` (environment).

| Mode | Best for | How users sign in |
| :--- | :--- | :--- |
| `basic` *(Helm default)* | Small teams, quick setups | Browser username/password prompt |
| `token` | Shared access and automation | Login page with an access token, or `Authorization: Bearer` for scripts |
| `oidc` | Company single sign-on | Keycloak, Microsoft Entra ID, Google, Okta, Dex, … |
| `openshift` | OpenShift clusters | OpenShift login, via an oauth-proxy sidecar |
| `header` | An existing auth proxy | oauth2-proxy, Authelia, … sets a user header |
| `none` *(binary default)* | Local development only | No authentication |

Only the dashboard and its API (port `8081`) are protected. Proxied application traffic (port `8080`) is never affected.

## Basic

```yaml
auth:
  mode: basic
  basic:
    username: admin
    password: ""   # empty = generated
```

Read the generated password:

```bash
kubectl get secret -n <namespace> <release>-smart-proxy-auth -o jsonpath='{.data.basic-password}' | base64 -d
```

## Token

```yaml
auth:
  mode: token
  token:
    token: ""   # empty = generated
```

Users paste the token on the login page and get a session cookie (valid for `auth.sessionTTL`, default `12h`). Scripts can call the API directly:

```bash
curl -H "Authorization: Bearer $TOKEN" https://smart-proxy-admin.example.com/api/routes
```

## OpenID Connect (SSO)

Create a confidential client in your identity provider with the redirect URI `https://<dashboard host>/auth/callback`, then:

```yaml
route:            # or ingress
  enabled: true
  host: smart-proxy-admin.apps.example.com

auth:
  mode: oidc
  oidc:
    issuerURL: https://keycloak.example.com/realms/main
    clientID: smart-proxy
    clientSecret: <secret>
    # Who may sign in (a match on any list is enough):
    allowedGroups: [platform-team]
    allowedDomains: [example.com]
    allowedEmails: [alice@partner.org]
```

- At least one allow-list is required. To admit every user of the identity provider, set `allowAll: true` explicitly. With a public issuer like Google, that means *any* Google account.
- Email and domain rules only match verified addresses (`email_verified`).
- Groups are read from the `groups` claim (`groupsClaim`). In Keycloak, add a *Group Membership* mapper; in Entra ID, enable the groups claim on the app registration.
- The login uses the authorization code flow with PKCE, and validates state and nonce.

| Provider | `issuerURL` |
| :--- | :--- |
| Keycloak | `https://<host>/realms/<realm>` |
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant-id>/v2.0` |
| Google | `https://accounts.google.com` |
| Okta | `https://<org>.okta.com` |
| Dex | the `issuer` in your Dex config |

## OpenShift

```yaml
route:
  enabled: true
  host: smart-proxy-admin.apps.example.com

auth:
  mode: openshift
```

The chart adds an [oauth-proxy](https://github.com/openshift/oauth-proxy) sidecar in front of the dashboard and registers the ServiceAccount as an OAuth client. Users sign in with their OpenShift account. By default, only users who can update Services in the release namespace get in (for example, holders of the `edit` or `admin` role). Change this with `auth.openshift.sar`, for example:

```yaml
auth:
  openshift:
    sar: '{"namespace":"smart-proxy","resource":"deployments","verb":"patch"}'
```

The dashboard itself then listens on `127.0.0.1` only, so the sidecar cannot be bypassed.

## Header (external auth proxy)

If an authenticating proxy already sits in front of the dashboard, Smart Proxy can trust the user header it sets:

```yaml
auth:
  mode: header
  header:
    userHeader: X-Forwarded-User
    logoutURL: /oauth2/sign_out
```

!!! warning
    Anyone who can reach the admin port directly can forge this header. Make sure only your proxy can connect to it, for example with a NetworkPolicy, or by running the proxy as a sidecar with `ADMIN_ADDR=127.0.0.1:8081`.

## Secrets and GitOps

Generated passwords, tokens and session keys are stored in the `<release>-smart-proxy-auth` Secret and reused on `helm upgrade`. Tools that render charts without cluster access (Argo CD, `helm template`) cannot reuse them and would rotate them on every sync. In that case, create your own Secret and reference it:

```yaml
auth:
  existingSecret: smart-proxy-auth
```

The keys read from it depend on the mode: `basic-password`, `token`, `oidc-client-secret`, `session-secret` (optional) and `cookie-secret` (openshift: 16, 24 or 32 characters).

## Environment variables

These are set by the Helm chart. Set them yourself when you deploy without it.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `AUTH_MODE` | `none`, `basic`, `token`, `oidc` or `header` (the chart's `openshift` mode runs as `header` behind the sidecar). | `none` |
| `AUTH_BASIC_USERNAME` / `AUTH_BASIC_PASSWORD` | Basic credentials. | `admin` / — |
| `AUTH_TOKEN` | Access token for `token` mode. | — |
| `AUTH_SESSION_SECRET` | Key signing session cookies. Random if unset, which signs everyone out on restart. | random |
| `AUTH_SESSION_TTL` | Session lifetime. | `12h` |
| `AUTH_OIDC_ISSUER_URL`, `AUTH_OIDC_CLIENT_ID`, `AUTH_OIDC_CLIENT_SECRET`, `AUTH_OIDC_REDIRECT_URL` | OIDC client settings. | — |
| `AUTH_OIDC_SCOPES` | Requested scopes. | `openid,email,profile` |
| `AUTH_OIDC_USERNAME_CLAIM` / `AUTH_OIDC_GROUPS_CLAIM` | Claims for the display name and the groups. | `email` / `groups` |
| `AUTH_OIDC_ALLOWED_EMAILS` / `_DOMAINS` / `_GROUPS` | Comma-separated allow-lists. | — |
| `AUTH_OIDC_ALLOW_ALL` | Admit every authenticated user. | `false` |
| `AUTH_HEADER_USER` / `AUTH_HEADER_LOGOUT_URL` | Trusted user header and optional sign-out URL. | `X-Forwarded-User` / — |
| `ADMIN_ADDR` | Listen address of the dashboard. | `:8081` |
