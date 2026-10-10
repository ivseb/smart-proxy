import type { Condition, ConditionField, ConditionOp, RouteConfig, WeightedBackend } from "@/types/api";
import { splitHosts } from "@/lib/format";
import { msg, t } from "@/lib/i18n";

// Labels are translated with t() where shown; noun is the field as said in a sentence.
export const FIELDS: { value: ConditionField; label: string; noun: string; named: boolean; placeholder: string }[] = [
    { value: "header", label: msg("Header"), noun: msg("header"), named: true, placeholder: "X-App-Version" },
    { value: "cookie", label: msg("Cookie"), noun: msg("cookie"), named: true, placeholder: "beta" },
    { value: "query", label: msg("Query parameter"), noun: msg("query parameter"), named: true, placeholder: "variant" },
    { value: "path", label: msg("Path"), noun: msg("path"), named: false, placeholder: "" },
    { value: "client", label: msg("Client IP"), noun: msg("client IP"), named: false, placeholder: "" },
];

const IS = msg("is"), CONTAINS = msg("contains"), STARTS_WITH = msg("starts with"), PRESENT = msg("is present");

export const OPS: Record<ConditionField, { value: ConditionOp; label: string }[]> = {
    header: [{ value: "equals", label: IS }, { value: "contains", label: CONTAINS }, { value: "prefix", label: STARTS_WITH }, { value: "exists", label: PRESENT }],
    cookie: [{ value: "equals", label: IS }, { value: "contains", label: CONTAINS }, { value: "prefix", label: STARTS_WITH }, { value: "exists", label: PRESENT }],
    query: [{ value: "equals", label: IS }, { value: "contains", label: CONTAINS }, { value: "prefix", label: STARTS_WITH }, { value: "exists", label: PRESENT }],
    path: [{ value: "prefix", label: STARTS_WITH }, { value: "equals", label: IS }],
    client: [{ value: "equals", label: msg("is in") }],
};

// describeCondition says a condition in words: "header X-App-Version starts with 5.".
export function describeCondition(c: Condition): string {
    const field = FIELDS.find(f => f.value === c.field);
    const label = OPS[c.field]?.find(o => o.value === c.op)?.label;
    const op = label ? t(label) : c.op;
    const noun = field ? t(field.noun) : c.field.toLowerCase();
    const subject = field?.named ? `${noun} ${c.name}` : noun;
    return c.op === "exists" ? `${subject} ${op}` : `${subject} ${op} ${c.value}`;
}

// conditionError says what is missing in a condition, or null.
export function conditionError(c: Condition): string | null {
    const field = FIELDS.find(f => f.value === c.field);
    if (field?.named && !c.name?.trim()) return t("Name the {field}", { field: t(field.noun) });
    if (c.op !== "exists" && !c.value?.trim()) return t("Enter a value");
    if (c.field === "path" && !c.value!.startsWith("/")) return t("A path starts with /");
    if (c.field === "client" && !/^[0-9a-fA-F:.]+(\/\d+)?$/.test(c.value!.trim())) return t("An IP or CIDR, e.g. 10.0.0.0/8");
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
    if (b.weight <= 0) return (b.when?.length || 0) > 0 ? t("only requests matching its conditions") : t("no traffic (weight 0, no conditions)");
    return t("{pct}% of the other requests", { pct: Math.round((b.weight * 100) / (total || 1)) });
}
