export interface DependencyConfig {
    name: string;
    stop_on_idle: boolean;
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
}

export interface RouteStatus extends RouteConfig {
    status: "Ready" | "Scaling" | "Sleep" | "Error" | "Unknown";
    dependency_status: Record<string, string>;
}

export interface LogEntry {
    timestamp: string;
    level: string;
    message: string;
    component?: string;
}

export interface StatsData {
    TotalRequests: number;
    RouteStats: Record<string, number>;
}
