import { useState } from "react";
import { Check, Pencil, Plus, Trash2, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { usePolling } from "@/hooks/usePolling";
import { useNow } from "@/hooks/useNow";
import { formatRelative } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { RULE_KINDS, addRule, kindLabel, removeRule, replaceRule, ruleError, ruleList } from "@/lib/rules";
import type { IgnoreKind, IgnoreRuleView, IgnoreRulesView, RouteConfig, RouteStatus, TrafficRules } from "@/types/api";

const inputClass = "bg-gray-900 border border-gray-700 rounded-lg px-2.5 py-1.5 text-sm text-gray-100 focus:outline-none focus:ring-2 focus:ring-blue-500";

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

// RulesListEditor edits a route's ignore rules as a list: add, change, remove, one rule per row.
// With hits, each rule shows what it matched over the last 24 hours.
export function RulesListEditor({ rules, onChange, hits, readOnly }: {
    rules: TrafficRules | null | undefined;
    onChange: (rules: TrafficRules) => void | Promise<unknown>;
    hits?: Map<string, IgnoreRuleView>;
    readOnly?: boolean;
}) {
    const now = useNow(10000);
    const [kind, setKind] = useState<IgnoreKind>("paths");
    const [value, setValue] = useState("");
    const [editing, setEditing] = useState<{ kind: IgnoreKind; rule: string; value: string } | null>(null);
    const rows = ruleList(rules);
    const error = value.trim() ? ruleError(kind, value) : null;
    const duplicate = rows.some(r => r.kind === kind && r.rule === value.trim());

    const add = async () => {
        if (!value.trim() || error || duplicate) return;
        await onChange(addRule(rules, kind, value));
        setValue("");
    };
    const saveEdit = async () => {
        if (!editing || ruleError(editing.kind, editing.value)) return;
        await onChange(replaceRule(rules, editing.kind, editing.rule, editing.value));
        setEditing(null);
    };

    return (
        <div className="space-y-2">
            {rows.length === 0 ? (
                <p className="text-sm text-gray-500 italic">{t("No rules of its own yet.")}</p>
            ) : (
                <ul className="divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                    {rows.map(r => {
                        const isEditing = editing?.kind === r.kind && editing.rule === r.rule;
                        const editError = isEditing ? ruleError(editing.kind, editing.value) : null;
                        return (
                            <li key={ruleKey(r.kind, r.rule)} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                                <span className="text-[11px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 whitespace-nowrap">{kindLabel(r.kind)}</span>
                                {isEditing ? (
                                    <input autoFocus className={`${inputClass} flex-1 min-w-[10rem] font-mono py-1`} value={editing.value}
                                        aria-label={t("Rule")} title={editError || undefined}
                                        onChange={e => setEditing({ ...editing, value: e.target.value })}
                                        onKeyDown={e => {
                                            if (e.key === "Enter") { e.preventDefault(); saveEdit(); }
                                            if (e.key === "Escape") { e.preventDefault(); setEditing(null); }
                                        }} />
                                ) : (
                                    <span className="flex-1 min-w-0 font-mono text-sm text-gray-100 break-all">{r.rule}</span>
                                )}
                                <Hits view={hits?.get(ruleKey(r.kind, r.rule))} now={now} />
                                {!readOnly && (
                                    <span className="flex items-center gap-1 ml-auto">
                                        {isEditing ? (
                                            <>
                                                <Button type="button" variant="ghost" size="icon" className="p-1" onClick={saveEdit} disabled={!!editError} aria-label={t("Save")}><Check size={14} /></Button>
                                                <Button type="button" variant="ghost" size="icon" className="p-1" onClick={() => setEditing(null)} aria-label={t("Cancel")}><X size={14} /></Button>
                                            </>
                                        ) : (
                                            <>
                                                <Button type="button" variant="ghost" size="icon" className="p-1" onClick={() => setEditing({ ...r, value: r.rule })} aria-label={t("Edit {rule}", { rule: r.rule })}><Pencil size={14} /></Button>
                                                <Button type="button" variant="ghost" size="icon" className="p-1 text-gray-400 hover:text-red-300" onClick={() => onChange(removeRule(rules, r.kind, r.rule))} aria-label={t("Remove {rule}", { rule: r.rule })}><Trash2 size={14} /></Button>
                                            </>
                                        )}
                                    </span>
                                )}
                            </li>
                        );
                    })}
                </ul>
            )}
            {!readOnly && (
                <div className="space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                        <select className={inputClass} value={kind} onChange={e => setKind(e.target.value as IgnoreKind)} aria-label={t("Kind of rule")}>
                            {RULE_KINDS.map(k => <option key={k.kind} value={k.kind}>{t(k.label)}</option>)}
                        </select>
                        <input className={`${inputClass} flex-1 min-w-[10rem] font-mono`} value={value} placeholder={RULE_KINDS.find(k => k.kind === kind)?.placeholder}
                            aria-label={t("Rule")} onChange={e => setValue(e.target.value)}
                            onKeyDown={e => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
                        <Button type="button" size="sm" variant="secondary" className="gap-1.5" onClick={add} disabled={!value.trim() || !!error || duplicate}>
                            <Plus size={14} /> {t("Add")}
                        </Button>
                    </div>
                    {(error || duplicate) && <p className="text-xs text-orange-300">{error || t("Already in the list")}</p>}
                    {kind === "paths" && !error && <p className="text-xs text-gray-500">{t("Exact path, or a prefix ending with * (e.g. /health*).")}</p>}
                </div>
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
