import type { Condition, ConditionField, ConditionOp, RouteConfig, WeightedBackend } from "@/types/api";
import { splitHosts } from "@/lib/format";

export const FIELDS: { value: ConditionField; label: string; named: boolean; placeholder: string }[] = [
    { value: "header", label: "Header", named: true, placeholder: "Origin" },
    { value: "cookie", label: "Cookie", named: true, placeholder: "beta" },
    { value: "query", label: "Query parameter", named: true, placeholder: "variant" },
    { value: "path", label: "Path", named: false, placeholder: "" },
    { value: "client", label: "Client IP", named: false, placeholder: "" },
];

export const OPS: Record<ConditionField, { value: ConditionOp; label: string }[]> = {
    header: [{ value: "equals", label: "is" }, { value: "contains", label: "contains" }, { value: "prefix", label: "starts with" }, { value: "exists", label: "is present" }],
    cookie: [{ value: "equals", label: "is" }, { value: "contains", label: "contains" }, { value: "prefix", label: "starts with" }, { value: "exists", label: "is present" }],
    query: [{ value: "equals", label: "is" }, { value: "contains", label: "contains" }, { value: "prefix", label: "starts with" }, { value: "exists", label: "is present" }],
    path: [{ value: "prefix", label: "starts with" }, { value: "equals", label: "is" }],
    client: [{ value: "equals", label: "is in" }],
};

// describeCondition says a condition in words: "header Origin is https://idp.example.com".
export function describeCondition(c: Condition): string {
    const field = FIELDS.find(f => f.value === c.field);
    const op = OPS[c.field]?.find(o => o.value === c.op)?.label || c.op;
    const subject = field?.named ? `${field.label.toLowerCase()} ${c.name}` : (field?.label || c.field).toLowerCase();
    return c.op === "exists" ? `${subject} ${op}` : `${subject} ${op} ${c.value}`;
}

// conditionError says what is missing in a condition, or null.
export function conditionError(c: Condition): string | null {
    const field = FIELDS.find(f => f.value === c.field);
    if (field?.named && !c.name?.trim()) return `Name the ${field.label.toLowerCase()}`;
    if (c.op !== "exists" && !c.value?.trim()) return "Enter a value";
    if (c.field === "path" && !c.value!.startsWith("/")) return "A path starts with /";
    if (c.field === "client" && !/^[0-9a-fA-F:.]+(\/\d+)?$/.test(c.value!.trim())) return "An IP or CIDR, e.g. 10.0.0.0/8";
    return null;
}

// testerLink is the URL pinning a browser to a backend for 12 hours.
export function testerLink(route: RouteConfig, service: string): string | null {
    const host = splitHosts(route.host)[0];
    if (!host) return null;
    const base = (route.path || "/").replace(/\/$/, "");
    return `https://${host.replace(/^\*\./, "")}${base}/__smart_proxy/use/${service}`;
}

// mainBackend is the route's single target, as a backend.
export function mainBackend(route: RouteConfig): WeightedBackend {
    return { service: route.target_service, port: route.target_port, weight: 100, workload: route.deployment, managed: true };
}

// shareText says how much traffic a backend gets.
export function shareText(b: WeightedBackend, all: WeightedBackend[]): string {
    const total = all.reduce((sum, x) => sum + (x.weight > 0 ? x.weight : 0), 0);
    if (b.weight <= 0) return (b.when?.length || 0) > 0 ? "only requests matching its conditions" : "no traffic (weight 0, no conditions)";
    return `${Math.round((b.weight * 100) / (total || 1))}% of the other requests`;
}
