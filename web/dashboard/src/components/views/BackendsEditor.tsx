import { useEffect, useState } from "react";
import { Copy, Plus, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/Button";
import type { Condition, ConditionField, RouteConfig, RouteStatus, ServiceInfo, WeightedBackend } from "@/types/api";
import { FIELDS, OPS, conditionError, describeCondition, mainBackend, shareText, testerLink } from "@/lib/backends";
import { workloadLabel } from "@/lib/format";
import { copyText } from "@/lib/clipboard";

const inputClass = "bg-gray-700 border border-gray-600 rounded-lg px-2.5 py-1.5 text-sm text-white focus:ring-2 focus:ring-blue-500 outline-none";

interface BackendsEditorProps {
    route: RouteStatus;
    // A condition to place, e.g. from a recorded request ("send requests like this to…").
    prefill?: Condition | null;
    onClose: () => void;
    onSave: (route: Partial<RouteConfig>) => Promise<boolean>;
}

// BackendsEditor edits where a route's requests go: its backends, their weights, and the
// conditions that send requests to one of them whatever the weights.
export function BackendsEditor({ route, prefill, onClose, onSave }: BackendsEditorProps) {
    const [backends, setBackends] = useState<WeightedBackend[]>(() =>
        route.backends?.length ? route.backends.map(b => ({ ...b, when: [...(b.when || [])] })) : [mainBackend(route)]);
    const [services, setServices] = useState<ServiceInfo[]>([]);
    const [toAdd, setToAdd] = useState("");
    const [pending, setPending] = useState<Condition | null>(prefill || null);
    const [pendingTarget, setPendingTarget] = useState("");
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        fetch(`/api/k8s/services?${new URLSearchParams({ namespace: route.namespace })}`)
            .then(res => res.ok ? res.json() : [])
            .then(setServices)
            .catch(() => setServices([]));
    }, [route.namespace]);

    const update = (idx: number, change: Partial<WeightedBackend>) =>
        setBackends(list => list.map((b, i) => (i === idx ? { ...b, ...change } : b)));
    const setCondition = (idx: number, ci: number, change: Partial<Condition>) =>
        update(idx, { when: (backends[idx].when || []).map((c, j) => (j === ci ? { ...c, ...change } : c)) });

    const available = services.filter(s => !backends.some(b => b.service === s.name));
    const addBackend = () => {
        const svc = services.find(s => s.name === toAdd);
        if (!svc) return;
        const added: WeightedBackend = { service: svc.name, port: svc.ports[0]?.port || 80, weight: 0, workload: svc.workload, managed: false, when: [] };
        if (pending) {
            added.when = [pending];
            setPending(null);
        }
        setBackends(list => [...list, added]);
        setToAdd("");
    };
    const applyPending = () => {
        const idx = backends.findIndex(b => b.service === pendingTarget);
        if (idx < 0 || !pending) return;
        update(idx, { when: [...(backends[idx].when || []), pending] });
        setPending(null);
    };

    const errors: string[] = [];
    if (!backends.some(b => b.managed)) errors.push("At least one backend must sleep and wake with the app (managed).");
    if (!backends.some(b => b.weight > 0)) errors.push("At least one backend needs a weight above 0, for requests matching no condition.");
    backends.forEach(b => (b.when || []).forEach(c => {
        const e = conditionError(c);
        if (e) errors.push(`${b.service}: ${e}.`);
    }));

    const save = async () => {
        setSaving(true);
        const cleaned = backends.map(b => ({ ...b, when: b.when?.length ? b.when : undefined }));
        // One backend without conditions is a plain route again.
        const single = cleaned.length === 1 && !cleaned[0].when;
        const data: Partial<RouteConfig> = single
            ? { ...route, backends: [], target_service: cleaned[0].service, target_port: cleaned[0].port, deployment: cleaned[0].workload || route.deployment }
            : { ...route, backends: cleaned };
        if (await onSave(data)) onClose();
        setSaving(false);
    };

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm" role="dialog" aria-modal="true">
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-3xl max-h-[90vh] overflow-y-auto">
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-center">
                    <h2 className="text-lg font-bold text-white">Where requests go</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label="Close"><X className="w-6 h-6" /></button>
                </div>
                <div className="p-6 space-y-5">
                    <p className="text-sm text-gray-400">
                        Each request goes to the <span className="text-gray-200">first backend whose conditions it matches</span>. The
                        others are shared by weight among the backends that are running. Either way, the browser then stays on the same
                        backend, so a login or a session started there continues there.
                    </p>

                    {pending && (
                        <div className="bg-blue-900/20 border border-blue-800 rounded-lg px-4 py-3 text-sm space-y-2">
                            <p className="text-blue-100">Send requests where <span className="font-mono">{describeCondition(pending)}</span> to:</p>
                            {backends.length > 1 ? (
                                <div className="flex flex-wrap gap-2">
                                    <select className={inputClass} value={pendingTarget} onChange={e => setPendingTarget(e.target.value)} aria-label="Backend for the condition">
                                        <option value="">Choose a backend…</option>
                                        {backends.map(b => <option key={b.service} value={b.service}>{b.service}</option>)}
                                    </select>
                                    <Button size="sm" onClick={applyPending} disabled={!pendingTarget}>Add the condition</Button>
                                    <Button size="sm" variant="ghost" onClick={() => setPending(null)}>Cancel</Button>
                                </div>
                            ) : (
                                <p className="text-blue-200/80">First add the backend that should receive them, below: the condition is added to it.</p>
                            )}
                        </div>
                    )}

                    {backends.map((b, idx) => {
                        const link = (b.weight === 0 || (b.when?.length || 0) > 0 || backends.length > 1) ? testerLink(route, b.service) : null;
                        return (
                            <section key={b.service} className="bg-gray-700/30 border border-gray-700 rounded-lg p-4 space-y-3">
                                <div className="flex items-start justify-between gap-3">
                                    <div className="min-w-0">
                                        <p className="font-mono text-white truncate">{b.service}:{b.port}</p>
                                        <p className="text-xs text-gray-500">{b.workload ? workloadLabel(b.workload) : "workload unknown"} · {shareText(b, backends)}</p>
                                    </div>
                                    {backends.length > 1 && (
                                        <Button variant="ghost" size="icon" onClick={() => setBackends(list => list.filter((_, i) => i !== idx))} aria-label={`Remove ${b.service}`}>
                                            <Trash2 size={16} />
                                        </Button>
                                    )}
                                </div>

                                <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
                                    <label className="flex items-center gap-2 text-gray-300">
                                        Weight
                                        <input type="number" min={0} max={256} value={b.weight} className={`${inputClass} w-20`}
                                            onChange={e => update(idx, { weight: Math.max(0, Math.min(256, Number(e.target.value) || 0)) })} />
                                    </label>
                                    <label className="flex items-center gap-2 text-gray-300 cursor-pointer" title="Managed backends are woken by requests and put to sleep with the app; others are never touched and get traffic only while they run.">
                                        <input type="checkbox" className="w-4 h-4 rounded bg-gray-700 border-gray-600" checked={b.managed}
                                            onChange={e => update(idx, { managed: e.target.checked })} />
                                        Sleeps and wakes with the app
                                    </label>
                                </div>

                                <div className="space-y-2">
                                    <p className="text-xs text-gray-400">
                                        {(b.when?.length || 0) > 0 ? "Send requests here when any of these is true:" : "No conditions: it only gets its share by weight."}
                                    </p>
                                    {(b.when || []).map((c, ci) => {
                                        const field = FIELDS.find(f => f.value === c.field)!;
                                        const err = conditionError(c);
                                        return (
                                            <div key={ci} className="flex flex-wrap items-center gap-2">
                                                <select className={inputClass} value={c.field} aria-label="Field"
                                                    onChange={e => {
                                                        const f = e.target.value as ConditionField;
                                                        setCondition(idx, ci, { field: f, op: OPS[f][0].value, name: FIELDS.find(x => x.value === f)!.named ? c.name : undefined });
                                                    }}>
                                                    {FIELDS.map(f => <option key={f.value} value={f.value}>{f.label}</option>)}
                                                </select>
                                                {field.named && (
                                                    <input className={`${inputClass} w-36`} placeholder={field.placeholder} value={c.name || ""} aria-label="Name"
                                                        onChange={e => setCondition(idx, ci, { name: e.target.value })} />
                                                )}
                                                <select className={inputClass} value={c.op} aria-label="Comparison"
                                                    onChange={e => setCondition(idx, ci, { op: e.target.value as Condition["op"] })}>
                                                    {OPS[c.field].map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                                                </select>
                                                {c.op !== "exists" && (
                                                    <input className={`${inputClass} flex-1 min-w-[12rem] font-mono`} value={c.value || ""} aria-label="Value"
                                                        placeholder={c.field === "path" ? "/saml/acs" : c.field === "client" ? "10.0.0.0/8" : "value"}
                                                        onChange={e => setCondition(idx, ci, { value: e.target.value })} />
                                                )}
                                                <Button variant="ghost" size="icon" onClick={() => update(idx, { when: (b.when || []).filter((_, j) => j !== ci) })} aria-label="Remove condition">
                                                    <X size={14} />
                                                </Button>
                                                {err && <span className="basis-full text-xs text-red-400">{err}</span>}
                                            </div>
                                        );
                                    })}
                                    <Button variant="secondary" size="sm" className="gap-1.5"
                                        onClick={() => update(idx, { when: [...(b.when || []), { field: "header", name: "", op: "equals", value: "" }] })}>
                                        <Plus size={14} /> Add condition
                                    </Button>
                                </div>

                                {link && (
                                    <div className="text-xs text-gray-400 bg-gray-900/50 rounded-lg px-3 py-2 space-y-1">
                                        <p>To try this backend in a browser, open (it stays chosen for 12 hours):</p>
                                        <div className="flex items-center gap-2">
                                            <span className="font-mono text-gray-200 break-all">{link}</span>
                                            <Button variant="ghost" size="icon" onClick={() => copyText(link)} aria-label="Copy link"><Copy size={14} /></Button>
                                        </div>
                                        <p className="text-gray-500">Replace <span className="font-mono">{b.service}</span> with <span className="font-mono">default</span> to go back.</p>
                                    </div>
                                )}
                            </section>
                        );
                    })}

                    <div className="flex flex-wrap items-center gap-2">
                        <select className={inputClass} value={toAdd} onChange={e => setToAdd(e.target.value)} aria-label="Service to add">
                            <option value="">Add a backend (Service in {route.namespace})…</option>
                            {available.map(s => <option key={s.name} value={s.name}>{s.name}{s.workload ? ` · ${workloadLabel(s.workload)}` : ""}</option>)}
                        </select>
                        <Button size="sm" className="gap-1.5" onClick={addBackend} disabled={!toAdd}><Plus size={14} /> Add</Button>
                        <span className="text-xs text-gray-500">Added with weight 0: it only gets requests matching its conditions.</span>
                    </div>

                    {errors.length > 0 && (
                        <ul className="text-sm text-red-300 bg-red-900/20 border border-red-900 rounded-lg px-4 py-2 list-disc list-inside">
                            {errors.map(e => <li key={e}>{e}</li>)}
                        </ul>
                    )}
                </div>
                <div className="px-6 py-4 border-t border-gray-700 flex justify-end gap-2">
                    <Button variant="ghost" onClick={onClose}>Cancel</Button>
                    <Button onClick={save} disabled={saving || errors.length > 0}>{saving ? "Saving…" : "Save"}</Button>
                </div>
            </div>
        </div>
    );
}
