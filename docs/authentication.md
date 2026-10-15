# Authentication

The admin dashboard can patch Ingresses/Routes and scale Deployments, so it should never be reachable without authentication. Smart Proxy supports several modes; whoever installs it picks the one that fits their cluster with `auth.mode` (Helm) or `AUTH_MODE` (environment).

| Mode | Best for | How users sign in |
| :--- | :--- | :--- |
| `basic` *(Helm default)* | Small teams, quick setups | Browser username/password prompt |
| `token` | Shared access and automation | Login page with an access token, or `Authorization: Bearer` for scripts |
| `oidc` | Company single sign-on | Keycloak, Microsoft Entra ID, Google, Okta, Dex, … |
| `openshift` | OpenShift clusters | OpenShift login, built in (no sidecar) |
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

Users sign in with their OpenShift account, on the cluster's own login page. Smart Proxy does it itself: there is no sidecar, the pod has a single container (and no CVE to inherit from one).

How it works:

1. **Smart Proxy's ServiceAccount is the OAuth client.** The chart annotates it with the dashboard's Route (or Ingress address), which tells OpenShift where the client may send users back. The client's secret is the ServiceAccount's own token, mounted in the pod.
2. **Sign-in.** The dashboard sends the browser to the OAuth server (discovered from the API server), which authenticates the user and sends them back to `/auth/callback`. Smart Proxy exchanges the code for a token (scopes `user:info user:check-access`, with PKCE) and reads who the user is.
3. **Access check.** With that token, Smart Proxy asks the API server whether the user has the required permission (a `SelfSubjectAccessReview`, made as the user). Smart Proxy itself needs no cluster-wide permission. The user's token isn't kept: a signed session cookie follows, valid for `auth.sessionTTL`.

By default, only users who can update Services in the release namespace get in (holders of the `edit` or `admin` role there). Others see who they signed in as and which permission is missing. Change it with `auth.openshift.sar`, in the format of oauth-proxy's `--openshift-sar` (a JSON array for several checks, all required):

```yaml
auth:
  openshift:
    sar: '{"namespace":"tools","resource":"deployments","resourceAPIGroup":"apps","verb":"patch"}'
```

The permission is checked when users sign in: someone whose role is removed keeps their session until it expires.

!!! note "Certificates"
    The token exchange goes from the pod to the OAuth server's Route (`oauth-openshift.apps.<cluster>`). Smart Proxy trusts public CAs and the ServiceAccount's CA bundle, which on OpenShift 4 usually includes the cluster's ingress CA. If sign-in fails with a certificate error in the log, put the CA in a ConfigMap of the release namespace and set `auth.openshift.caConfigMap` (and `caKey`, `ca.crt` by default). If the OAuth server can't be discovered, set `auth.openshift.oauthURL`.

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

The keys read from it depend on the mode: `basic-password`, `token`, `oidc-client-secret` and `session-secret` (token, oidc and openshift; strongly recommended with more than one replica, so sessions work on all of them). A Secret made for the former oauth-proxy sidecar works as is: its `cookie-secret` signs the sessions.

## Environment variables

These are set by the Helm chart. Set them yourself when you deploy without it.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `AUTH_MODE` | `none`, `basic`, `token`, `oidc`, `openshift` or `header`. | `none` |
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
| `AUTH_OPENSHIFT_SAR` | Access review(s) to pass, oauth-proxy's JSON format. | update Services in Smart Proxy's namespace |
| `AUTH_OPENSHIFT_SAR_NAMESPACE` / `_RESOURCE` / `_VERB` | One access review, without JSON (when `AUTH_OPENSHIFT_SAR` is unset). | Smart Proxy's namespace / `services` / `update` |
| `AUTH_OPENSHIFT_SERVICE_ACCOUNT` | The OAuth client: a ServiceAccount name, or `system:serviceaccount:<ns>:<name>`. | Smart Proxy's own |
| `AUTH_OPENSHIFT_REDIRECT_URL` | Callback address (`https://<dashboard>/auth/callback`). | the address the dashboard is reached at |
| `AUTH_OPENSHIFT_BASE_URL` | The OAuth server, when it can't be discovered. | discovered |
| `AUTH_OPENSHIFT_API_URL` | The API server. | the cluster's, in a pod |
| `AUTH_OPENSHIFT_CA_FILE` | More CA certificates to trust (PEM file). | — |
| `ADMIN_ADDR` | Listen address of the dashboard. | `:8081` |
