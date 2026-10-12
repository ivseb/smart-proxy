import type { IgnoreKind, RuleSpec, TrafficRules } from "@/types/api";
import { msg, t } from "@/lib/i18n";

// The single-condition kinds of ignore rules, as listed for the global rules and probe paths.
const KIND_LABELS: Record<string, string> = {
    paths: msg("Path"),
    user_agents: msg("User agent contains"),
    sources: msg("Client IP or CIDR"),
    methods: msg("Method"),
    probes: msg("Probe path"),
    rules: msg("Rule"),
};

export const kindLabel = (kind: string) => KIND_LABELS[kind] ? t(KIND_LABELS[kind]) : kind;

export const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

const clean = (r: RuleSpec): RuleSpec => {
    const out: RuleSpec = {};
    if (r.path?.trim()) out.path = r.path.trim();
    if (r.methods?.length) out.methods = [...new Set(r.methods.map(m => m.trim().toUpperCase()).filter(Boolean))].sort();
    if (r.user_agent?.trim()) out.user_agent = r.user_agent.trim();
    if (r.source?.trim()) out.source = r.source.trim();
    return out;
};

// ruleString identifies a rule as Smart Proxy does (its matches are counted under this name).
export function ruleString(rule: RuleSpec): string {
    const r = clean(rule);
    return [
        r.path && `path=${r.path}`,
        r.methods?.length && `method=${r.methods.join(",")}`,
        r.user_agent && `ua=${r.user_agent}`,
        r.source && `ip=${r.source}`,
    ].filter(Boolean).join(" ");
}

// simple returns the single-condition list a rule fits in (one condition, one value), if any.
function simple(rule: RuleSpec): { kind: IgnoreKind; value: string } | null {
    const r = clean(rule);
    const set = [r.path, r.methods?.length ? r.methods : undefined, r.user_agent, r.source].filter(v => v !== undefined);
    if (set.length !== 1) return null;
    if (r.path) return { kind: "paths", value: r.path };
    if (r.user_agent) return { kind: "user_agents", value: r.user_agent };
    if (r.source) return { kind: "sources", value: r.source };
    return r.methods!.length === 1 ? { kind: "methods", value: r.methods![0] } : null;
}

// specsOf lists a route's ignore rules, whichever way they are stored.
export function specsOf(rules: TrafficRules | null | undefined): RuleSpec[] {
    return [
        ...(rules?.rules || []).map(clean),
        ...(rules?.paths || []).map(path => ({ path })),
        ...(rules?.user_agents || []).map(user_agent => ({ user_agent })),
        ...(rules?.sources || []).map(source => ({ source })),
        ...(rules?.methods || []).map(m => ({ methods: [m] })),
    ];
}

// toTrafficRules stores rules: one condition in the simple lists (as annotations and Helm values
// write them), several as combined rules. Duplicates are dropped.
export function toTrafficRules(specs: RuleSpec[]): TrafficRules {
    const out: Required<TrafficRules> = { paths: [], user_agents: [], sources: [], methods: [], rules: [] };
    const seen = new Set<string>();
    for (const spec of specs) {
        const key = ruleString(spec);
        if (!key || seen.has(key)) continue;
        seen.add(key);
        const s = simple(spec);
        if (s) out[s.kind].push(s.value);
        else out.rules.push(clean(spec));
    }
    return out;
}

// specKey names a rule the way its matches are reported.
export function specKey(spec: RuleSpec): string {
    const s = simple(spec);
    return s ? `${s.kind} ${s.value}` : `rules ${ruleString(spec)}`;
}

// specError says what is wrong with a rule, or null.
export function specError(spec: RuleSpec): string | null {
    const r = clean(spec);
    if (!r.path && !r.methods?.length && !r.user_agent && !r.source) return t("Fill in at least one condition");
    if (r.path && !r.path.startsWith("/")) return t("A path starts with /");
    if (r.source && !/^[0-9a-fA-F:.]+(\/\d+)?$/.test(r.source)) return t("An IP or CIDR, e.g. 10.0.0.0/8");
    return null;
}

// addRule returns the rules with one more single-condition rule (unchanged if already there).
export function addRule(rules: TrafficRules | null | undefined, kind: IgnoreKind, value: string): TrafficRules {
    const spec: RuleSpec = kind === "paths" ? { path: value } : kind === "user_agents" ? { user_agent: value } : kind === "sources" ? { source: value } : { methods: [value] };
    return toTrafficRules([...specsOf(rules), spec]);
}

// userAgentToken is the part of a User-Agent worth an ignore rule ("kube-probe" in
// "kube-probe/1.33"), or null for browsers, whose User-Agent says "Mozilla".
export function userAgentToken(ua?: string): string | null {
    if (!ua) return null;
    const inner = ua.match(/compatible;\s*([A-Za-z][A-Za-z0-9._+-]*)/);
    const token = inner ? inner[1] : ua.match(/^[A-Za-z][A-Za-z0-9._+-]*/)?.[0];
    return token && token.toLowerCase() !== "mozilla" ? token : null;
}
