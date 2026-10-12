import { useState } from "react";
import { Check, Pencil, Plus, Trash2, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { usePolling } from "@/hooks/usePolling";
import { useNow } from "@/hooks/useNow";
import { formatRelative } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { METHODS, kindLabel, specError, specKey, specsOf, toTrafficRules } from "@/lib/rules";
import type { IgnoreRuleView, IgnoreRulesView, RouteConfig, RouteStatus, RuleSpec, TrafficRules } from "@/types/api";

const inputClass = "w-full bg-gray-900 border border-gray-700 rounded-lg px-2.5 py-1.5 text-sm text-gray-100 font-mono placeholder:font-sans placeholder:text-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500";

const ruleKey = (kind: string, rule: string) => `${kind} ${rule}`;

// Hits shows what a rule matched over the last 24 hours.
function Hits({ view, now }: { view?: IgnoreRuleView; now: number }) {
    if (!view) return null;
    if (!view.hits) return <span className="text-xs text-gray-600 whitespace-nowrap">{t("no match in 24h")}</span>;
    return (
        <span className="text-xs text-gray-400 whitespace-nowrap tabular-nums" title={view.last ? new Date(view.last).toLocaleString() : undefined}>
            {tn(view.hits, "{n} request", "{n} requests")}
            {view.last && <span className="text-gray-500"> · {t("last {time}", { time: formatRelative(view.last, now) })}</span>}
        </span>
    );
}

// Conditions shows a rule's conditions as "Path /api/* · Method GET, POST".
function Conditions({ spec }: { spec: RuleSpec }) {
    const parts: [string, string][] = [];
    if (spec.path) parts.push([t("Path"), spec.path]);
    if (spec.methods?.length) parts.push([spec.methods.length > 1 ? t("Methods") : t("Method"), spec.methods.join(", ")]);
    if (spec.user_agent) parts.push([t("User agent contains"), spec.user_agent]);
    if (spec.source) parts.push([t("Client IP or CIDR"), spec.source]);
    return (
        <span className="flex flex-wrap items-center gap-1.5 min-w-0 flex-1">
            {parts.map(([label, value], i) => (
                <span key={label} className="inline-flex items-center gap-1.5 min-w-0">
                    {i > 0 && <span className="text-[11px] text-gray-500">{t("and")}</span>}
                    <span className="text-[11px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 whitespace-nowrap">{label}</span>
                    <span className="font-mono text-sm text-gray-100 break-all">{value}</span>
                </span>
            ))}
        </span>
    );
}

// RuleForm edits one rule: the conditions a request must all meet.
function RuleForm({ initial, submitLabel, onSubmit, onCancel }: {
    initial?: RuleSpec;
    submitLabel: string;
    onSubmit: (spec: RuleSpec) => void | Promise<unknown>;
    onCancel?: () => void;
}) {
    const [spec, setSpec] = useState<RuleSpec>(initial || {});
    const [touched, setTouched] = useState(false);
    const error = specError(spec);
    const set = (patch: Partial<RuleSpec>) => { setSpec(prev => ({ ...prev, ...patch })); setTouched(true); };
    const toggleMethod = (m: string) => {
        const methods = spec.methods || [];
        set({ methods: methods.includes(m) ? methods.filter(x => x !== m) : [...methods, m] });
    };
    const submit = async () => {
        if (error) return;
        await onSubmit(spec);
        if (!initial) { setSpec({}); setTouched(false); }
    };
    const onKey = (e: React.KeyboardEvent) => {
        if (e.key === "Enter") { e.preventDefault(); submit(); }
        if (e.key === "Escape" && onCancel) { e.preventDefault(); onCancel(); }
    };

    return (
        <div className="space-y-2 rounded-lg border border-gray-700/60 bg-gray-900/40 p-3">
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
                <label className="space-y-1">
                    <span className="block text-xs text-gray-400">{t("Path")}</span>
                    <input className={inputClass} value={spec.path || ""} placeholder="/healthz, /api/*" onChange={e => set({ path: e.target.value })} onKeyDown={onKey} />
                </label>
                <label className="space-y-1">
                    <span className="block text-xs text-gray-400">{t("User agent contains")}</span>
                    <input className={inputClass} value={spec.user_agent || ""} placeholder="MyMonitor" onChange={e => set({ user_agent: e.target.value })} onKeyDown={onKey} />
                </label>
                <label className="space-y-1">
                    <span className="block text-xs text-gray-400">{t("Client IP or CIDR")}</span>
                    <input className={inputClass} value={spec.source || ""} placeholder="10.20.0.0/16" onChange={e => set({ source: e.target.value })} onKeyDown={onKey} />
                </label>
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
                <span className="text-xs text-gray-400 mr-1">{t("Methods")}</span>
                {METHODS.map(m => {
                    const on = spec.methods?.includes(m);
                    return (
                        <button key={m} type="button" onClick={() => toggleMethod(m)} aria-pressed={on}
                            className={`font-mono text-xs px-2 py-1 rounded-md border transition-colors ${on ? "border-blue-500 bg-blue-500/20 text-blue-100" : "border-gray-700 text-gray-400 hover:text-white hover:border-gray-500"}`}>
                            {m}
                        </button>
                    );
                })}
                <span className="text-xs text-gray-500 ml-1">{spec.methods?.length ? "" : t("(any)")}</span>
                <span className="flex items-center gap-1 ml-auto">
                    {onCancel && <Button type="button" variant="ghost" size="sm" className="gap-1" onClick={onCancel}><X size={14} /> {t("Cancel")}</Button>}
                    <Button type="button" size="sm" variant="secondary" className="gap-1.5" onClick={submit} disabled={!!error}>
                        {initial ? <Check size={14} /> : <Plus size={14} />} {submitLabel}
                    </Button>
                </span>
            </div>
            <p className={`text-xs ${touched && error ? "text-orange-300" : "text-gray-500"}`}>
                {touched && error ? error : t("A request is ignored when it meets every condition filled in (any of the chosen methods). Paths: exact, or a prefix ending with *.")}
            </p>
        </div>
    );
}

// RulesListEditor edits a route's ignore rules as a list, one rule per row: add, change, remove.
// With hits, each rule shows what it matched over the last 24 hours.
export function RulesListEditor({ rules, onChange, hits, readOnly }: {
    rules: TrafficRules | null | undefined;
    onChange: (rules: TrafficRules) => void | Promise<unknown>;
    hits?: Map<string, IgnoreRuleView>;
    readOnly?: boolean;
}) {
    const now = useNow(10000);
    const [editing, setEditing] = useState<number | null>(null);
    const specs = specsOf(rules);

    const replace = (i: number, spec: RuleSpec | null) => {
        const next = [...specs];
        if (spec) next[i] = spec; else next.splice(i, 1);
        return onChange(toTrafficRules(next));
    };

    return (
        <div className="space-y-2">
            {specs.length === 0 ? (
                <p className="text-sm text-gray-500 italic">{t("No rules of its own yet.")}</p>
            ) : (
                <ul className="divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                    {specs.map((spec, i) => editing === i ? (
                        <li key={specKey(spec)} className="p-2">
                            <RuleForm initial={spec} submitLabel={t("Save")} onCancel={() => setEditing(null)}
                                onSubmit={async s => { await replace(i, s); setEditing(null); }} />
                        </li>
                    ) : (
                        <li key={specKey(spec)} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                            <Conditions spec={spec} />
                            <Hits view={hits?.get(specKey(spec))} now={now} />
                            {!readOnly && (
                                <span className="flex items-center gap-1 ml-auto">
                                    <Button type="button" variant="ghost" size="icon" className="p-1" onClick={() => setEditing(i)} aria-label={t("Edit {rule}", { rule: specKey(spec) })}><Pencil size={14} /></Button>
                                    <Button type="button" variant="ghost" size="icon" className="p-1 text-gray-400 hover:text-red-300" onClick={() => replace(i, null)} aria-label={t("Remove {rule}", { rule: specKey(spec) })}><Trash2 size={14} /></Button>
                                </span>
                            )}
                        </li>
                    ))}
                </ul>
            )}
            {!readOnly && editing === null && (
                <RuleForm submitLabel={t("Add rule")} onSubmit={spec => onChange(toTrafficRules([...specs, spec]))} />
            )}
        </div>
    );
}

// IgnoreRulesCard lists everything keeping a route's requests from counting as activity: its own
// rules (editable), its workloads' probe paths and the global rules, with what each matched.
export function IgnoreRulesCard({ route, onSave }: { route: RouteStatus; onSave: (route: Partial<RouteConfig>) => Promise<boolean> }) {
    const { data, refetch } = usePolling<IgnoreRulesView>(`/api/routes/ignore-rules?${new URLSearchParams({ id: route.id })}`, 10000);
    const now = useNow(10000);
    const hits = new Map<string, IgnoreRuleView>();
    for (const r of [...(data?.route || []), ...(data?.global || []), ...(data?.probes || [])]) hits.set(ruleKey(r.kind, r.rule), r);
    const inherited = [...(data?.probes || []), ...(data?.global || [])];
    const active = inherited.filter(r => r.hits > 0);
    // Probe paths and matching global rules are listed; the many quiet ones (known monitors) fold away.
    const shown = [...active, ...(data?.probes || []).filter(r => !r.hits)];
    const quiet = (data?.global || []).filter(r => !r.hits);
    const asleep = route.when_asleep === "unavailable" ? t("503 Service Unavailable") : route.when_asleep === "wake" ? t("wake the app") : t("200 OK from Smart Proxy");

    return (
        <Card>
            <CardHeader>
                <CardTitle>{t("Ignored requests")}</CardTitle>
                <p className="text-sm text-gray-400">
                    {t("Requests matching these rules don't count as activity and never wake the app. While it sleeps they get: {answer}.", { answer: asleep })}
                </p>
            </CardHeader>
            <CardContent className="space-y-4">
                <RulesListEditor
                    rules={route.ignore}
                    hits={hits}
                    readOnly={route.declarative}
                    onChange={async ignore => { if (await onSave({ ...route, ignore })) refetch(); }}
                />
                {route.declarative && <p className="text-xs text-gray-500">{t("Defined by annotations: change smart-proxy/ignore-* there.")}</p>}
                {inherited.length > 0 && (
                    <div className="space-y-2">
                        <p className="text-sm text-gray-400">
                            {t("Also applied")}: {tn(data?.probes.length || 0, "{n} probe path of its workloads", "{n} probe paths of its workloads")}, {tn(data?.global.length || 0, "{n} global rule", "{n} global rules")}
                            {active.length > 0 && <span className="text-gray-500"> · {tn(active.length, "{n} matched in 24h", "{n} matched in 24h")}</span>}
                        </p>
                        {shown.length > 0 && (
                            <ul className="divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                                {shown.map(r => (
                                    <li key={ruleKey(r.kind, r.rule)} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5">
                                        <span className="text-[11px] px-1.5 py-0.5 rounded bg-gray-700/60 text-gray-400 whitespace-nowrap">{kindLabel(r.kind)}</span>
                                        <span className="flex-1 min-w-0 font-mono text-sm text-gray-300 break-all">{r.rule}</span>
                                        <Hits view={r} now={now} />
                                    </li>
                                ))}
                            </ul>
                        )}
                        {quiet.length > 0 && (
                            <details className="text-xs text-gray-500">
                                <summary className="cursor-pointer hover:text-gray-300">
                                    {tn(quiet.length, "{n} more global rule, no match in 24h", "{n} more global rules, no match in 24h")}
                                </summary>
                                <p className="mt-1 leading-relaxed">{quiet.map(r => r.rule).join(" · ")}</p>
                            </details>
                        )}
                        <p className="text-xs text-gray-500">{t("Global rules come from Smart Proxy's Helm values (ignore.*) and its list of known monitors.")}</p>
                    </div>
                )}
            </CardContent>
        </Card>
    );
}
