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
}

export interface LogEntry {
    timestamp: string;
    message: string;
}

export interface StatsData {
    TotalRequests: number;
    RouteStats: Record<string, number>;
}
