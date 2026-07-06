# Architecture

Smart Proxy acts as a "Man-in-the-Middle" for your Kubernetes Ingresses and OpenShift Routes.

## Workflow

1.  **Patching**: When you "Patch" a route via the Admin UI, Smart Proxy modifies the Ingress/Route to point to its own service (`smart-proxy`) instead of the original application service.
    - The original configuration is saved in annotations (`smart-proxy/original-service`).
    - The `smart-proxy/patched` annotation is set to `true`.

2.  **Request Handling**:
    - Users access `app.example.com`.
    - Traffic hits `smart-proxy`.
    - The proxy checks the `Host` header to find the corresponding configuration.

3.  **Idle Detection**:
    - If the target deployment is scaled to 0 (sleeping), the proxy holds the request and triggers a scale-up.
    - It shows a "Waking Up" page to the user.
    - Once the deployment is ready, it proxies the request.
    - A timer tracks inactivity. If no requests occur within the `IdleTimeout`, the proxy scales the deployment back to 0.

4.  **Dependencies**:
    - If a route has dependencies configured, the proxy ensures all dependent services are running before forwarding traffic.
    - Usage of one service keeps the entire chain alive.
    - When the main service idles, dependencies can optionally be stopped as well.
