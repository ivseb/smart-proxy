export interface DependencyConfig {
    name: string;
    stop_on_idle: boolean;
}

export interface Schedule {
    days?: string[]; // "mon".."sun"; empty = every day
    from: string; // "HH:MM"
    to: string;
    timezone?: string; // IANA name
}

export interface TrafficRules {
    user_agents?: string[]; // Case-insensitive User-Agent substrings
    paths?: string[]; // Exact, or prefix ending with "*"
    sources?: string[]; // Client IPs or CIDRs
    methods?: string[];
}

export type WhenAsleep = "respond" | "unavailable" | "wake";

// A client sending requests to a route.
export interface TrafficSource {
    client: string; // Monitor/tool name, or "Browser"
    method: string;
    path: string;
    user_agent: string;
    last_ip: string;
    requests: number;
    ignored: number;
    reason?: string;
    first_seen: string;
    last_seen: string;
    interval_seconds: number;
    counts_as_activity: boolean;
}

// One of the Services an OpenShift Route balances traffic across.
export type ConditionField = "header" | "cookie" | "query" | "path" | "client";
export type ConditionOp = "equals" | "contains" | "prefix" | "exists";

// Sends matching requests to a backend, e.g. header Origin equals https://login.example.com.
export interface Condition {
    field: ConditionField;
    name?: string; // Header, cookie or query parameter name
    op: ConditionOp;
    value?: string;
}

export interface WeightedBackend {
    service: string;
    port: number;
    weight: number; // 0: only requests matching its conditions
    workload?: string;
    managed: boolean; // Sleeps and wakes with the route; others only get traffic while they run
    when?: Condition[] | null; // Any of these sends the request here (and keeps the client here)
}

export interface ServiceInfo {
    name: string;
    ports: { name?: string; port: number }[];
    workload?: string;
}

export interface RouteConfig {
    id: string;
    host: string;
    path: string;
    target_service: string;
    target_port: number;
    namespace: string;
    deployment: string;
    dependencies: DependencyConfig[];
    idle_timeout: number; // in nanoseconds
    last_activity: string;
    inject_badge: boolean;
    always_on?: boolean;
    start_in_order?: boolean;
    schedule?: Schedule | null;
    declarative?: boolean; // Defined by smart-proxy/* annotations on its Ingress/Route
    ignore?: TrafficRules | null; // Requests that don't count as activity (monitors…)
    when_asleep?: WhenAsleep | ""; // Answer to those requests while asleep ("" = respond)
    backends?: WeightedBackend[] | null; // Balanced Route: the Services and their weights
    inspect_until?: string | null; // Requests are recorded until then
    protection?: Protection | null;
}

// Require a login (browsers) or an access token (scripts) before requests reach the app.
export interface Protection {
    enabled: boolean;
    open?: string[]; // Paths reachable without login; prefixes end with "*"
    session_hours?: number; // Default 12
}

export interface Credential {
    name: string;
    created: string;
    hint?: string; // Last characters of a token
}

export type DeploymentStatus = "Ready" | "Scaling" | "Sleep" | "Error" | "Unwatched" | "Offline";

export interface ResourceRef {
    kind: "Ingress" | "Route";
    namespace: string;
    name: string;
}

export interface RouteStatus extends RouteConfig {
    status: DeploymentStatus;
    replicas: number;
    ready_replicas: number;
    dependency_status: Record<string, DeploymentStatus>;
    source: ResourceRef | null; // null for manually configured routes
    sleeps_at: string | null; // null when it never sleeps (Always On, manual, asleep)
    effective_idle_timeout: number; // nanoseconds
    schedule_active: boolean;
    resources: (ResourceRef & { host: string })[]; // Patched for this route; restored when it is deleted
    backend_status: (WeightedBackend & { status: DeploymentStatus | "Unknown"; share: number })[];
    protection_users: Credential[];
    protection_tokens: Credential[];
    protection_available: boolean; // Smart Proxy's Secret is usable
}

export interface DeploymentSummary {
    name: string;
    replicas: number;
    ready: number;
}

export interface PatchableResource {
    type: "Ingress" | "Route";
    name: string;
    namespace: string;
    host: string;
    path: string;
    service: string;
    port: string;
    patched: boolean;
    deployment: DeploymentSummary | null;
    route_id: string;
    status: string;
}

export interface ClusterInfo {
    connected: boolean;
    scope: string;
    all_namespaces: boolean;
    namespaces: string[];
    default: string;
    routes_enabled: boolean;
    proxy_service: string;
    replica: string;
    ignore_defaults?: TrafficRules; // Ignored for every route
}

export interface LogEntry {
    timestamp: string;
    message: string;
}

export interface StatsHistory {
    interval: number; // seconds per point
    retention?: number; // seconds
    points: { at: string; requests: number; routes?: Record<string, number> }[];
}

export interface StatsData {
    TotalRequests: number;
    RouteStats: Record<string, number>;
}

// What happened to a request, as recorded by the inspector.
export type RequestOutcome = "proxied" | "woken" | "waking_page" | "asleep" | "unavailable" | "login" | "denied" | "error";

export interface InspectedRequest {
    at: string;
    replica: string;
    method: string;
    host: string;
    path: string;
    query?: string[]; // Parameter names
    status: number;
    duration_ms: number;
    outcome: RequestOutcome;
    backend?: string;
    why?: string; // Why that backend: "split", "sticky", "condition", "link"
    ignored?: string; // Why it didn't count as activity
    user?: string;
    client: string;
    user_agent?: string;
    headers: Record<string, string>; // Credentials masked
    cookies?: string[]; // Names
}

export interface InspectedRequests {
    requests: InspectedRequest[];
    replicas: number;
}
