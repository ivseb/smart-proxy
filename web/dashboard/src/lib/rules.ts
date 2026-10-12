import type { IgnoreKind, TrafficRules } from "@/types/api";
import { msg, t } from "@/lib/i18n";

// The kinds of ignore rules: how they read, an example, and what a valid value looks like.
export const RULE_KINDS: { kind: IgnoreKind; label: string; placeholder: string }[] = [
    { kind: "paths", label: msg("Path"), placeholder: "/healthz, /status/*" },
    { kind: "user_agents", label: msg("User agent contains"), placeholder: "MyMonitor" },
    { kind: "sources", label: msg("Client IP or CIDR"), placeholder: "10.20.0.0/16" },
    { kind: "methods", label: msg("Method"), placeholder: "HEAD" },
];

export const kindLabel = (kind: string) => {
    const k = RULE_KINDS.find(r => r.kind === kind);
    return k ? t(k.label) : kind === "probes" ? t("Probe path") : kind;
};

// ruleError says what is wrong with a rule's value, or null.
export function ruleError(kind: IgnoreKind, value: string): string | null {
    const v = value.trim();
    if (!v) return t("Enter a value");
    if (kind === "paths" && !v.startsWith("/")) return t("A path starts with /");
    if (kind === "sources" && !/^[0-9a-fA-F:.]+(\/\d+)?$/.test(v)) return t("An IP or CIDR, e.g. 10.0.0.0/8");
    if (kind === "methods" && !/^[A-Za-z]+$/.test(v)) return t("A method, e.g. HEAD");
    return null;
}

// normalize puts a value in the form the rules keep (methods in capitals).
const normalize = (kind: IgnoreKind, value: string) => kind === "methods" ? value.trim().toUpperCase() : value.trim();

// addRule returns the rules with one more (unchanged if it is already there).
export function addRule(rules: TrafficRules | null | undefined, kind: IgnoreKind, value: string): TrafficRules {
    const v = normalize(kind, value);
    const list = rules?.[kind] || [];
    return list.includes(v) ? { ...(rules || {}) } : { ...(rules || {}), [kind]: [...list, v] };
}

export function removeRule(rules: TrafficRules | null | undefined, kind: IgnoreKind, value: string): TrafficRules {
    return { ...(rules || {}), [kind]: (rules?.[kind] || []).filter(v => v !== value) };
}

export function replaceRule(rules: TrafficRules | null | undefined, kind: IgnoreKind, old: string, value: string): TrafficRules {
    const v = normalize(kind, value);
    const list = (rules?.[kind] || []).map(x => x === old ? v : x);
    return { ...(rules || {}), [kind]: [...new Set(list)] };
}

// ruleList flattens rules into rows, paths first.
export function ruleList(rules: TrafficRules | null | undefined): { kind: IgnoreKind; rule: string }[] {
    return RULE_KINDS.flatMap(k => (rules?.[k.kind] || []).map(rule => ({ kind: k.kind, rule })));
}

// userAgentToken is the part of a User-Agent worth an ignore rule ("kube-probe" in
// "kube-probe/1.33"), or null for browsers, whose User-Agent says "Mozilla".
export function userAgentToken(ua?: string): string | null {
    if (!ua) return null;
    const inner = ua.match(/compatible;\s*([A-Za-z][A-Za-z0-9._+-]*)/);
    const token = inner ? inner[1] : ua.match(/^[A-Za-z][A-Za-z0-9._+-]*/)?.[0];
    return token && token.toLowerCase() !== "mozilla" ? token : null;
}
