import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Plus, X } from "lucide-react";
import type { ClusterInfo, RouteConfig, Schedule, TrafficRules, WhenAsleep } from "@/types/api";
import { Button } from "@/components/ui/Button";
import { formatDuration, parseDuration, splitHosts, workloadLabel } from "@/lib/format";

const DEFAULT_TIMEOUT = "30m";
const DAYS: { value: string; label: string }[] = [
    { value: "mon", label: "Mon" }, { value: "tue", label: "Tue" }, { value: "wed", label: "Wed" },
    { value: "thu", label: "Thu" }, { value: "fri", label: "Fri" }, { value: "sat", label: "Sat" }, { value: "sun", label: "Sun" },
];

function defaultSchedule(): Schedule {
    let timezone = "UTC";
    try {
        timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    } catch {
        // Keep UTC
    }
    return { days: ["mon", "tue", "wed", "thu", "fri"], from: "08:00", to: "19:00", timezone };
}

interface ConnectedResource {
    name: string;
    host: string;
    type: "Ingress" | "Route";
    share: number; // % of the Route's weight going to this Service
    alternate: boolean; // The Service is one of the Route's alternate backends
}

interface RouteModalProps {
    isOpen: boolean;
    onClose: () => void;
    onSubmit: (data: Partial<RouteConfig>) => Promise<boolean>;
    initialData?: RouteConfig | null;
    info: ClusterInfo | null;
}

const emptyRoute = (namespace: string): Partial<RouteConfig> => ({
    host: "",
    path: "/",
    namespace,
    deployment: "",
    target_service: "",
    target_port: 80,
    dependencies: [],
    inject_badge: false,
    always_on: false,
    idle_timeout: parseDuration(DEFAULT_TIMEOUT)!,
});

const inputClass = "w-full bg-gray-700 border border-gray-600 rounded-lg px-3 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white disabled:opacity-60";

async function getJSON<T>(url: string): Promise<T> {
    const res = await fetch(url);
    if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
    return res.json();
}

export function RouteModal({ isOpen, onClose, onSubmit, initialData, info }: RouteModalProps) {
    const [formData, setFormData] = useState<Partial<RouteConfig>>(emptyRoute(""));
    const [timeoutInput, setTimeoutInput] = useState(DEFAULT_TIMEOUT);
    const [deployments, setDeployments] = useState<string[]>([]);
    const [selectedDepToAdd, setSelectedDepToAdd] = useState("");
    const [resolvedInfo, setResolvedInfo] = useState<string | null>(null);
    const [connected, setConnected] = useState<ConnectedResource[]>([]);
    const [saving, setSaving] = useState(false);

    const namespaces = info?.namespaces || [];
    // A patched route belongs to its Ingress/Route; moving it to another namespace would break that link.
    const boundToResource = !!initialData && /^(ing|route)-/.test(initialData.id);
    const timeoutValid = parseDuration(timeoutInput) !== null;

    const loadDeployments = async (ns: string) => {
        if (!ns) return;
        try {
            setDeployments(await getJSON<string[]>(`/api/k8s/deployments?${new URLSearchParams({ namespace: ns })}`));
        } catch {
            setDeployments([]);
        }
    };

    const loadConnected = async (ns: string, service: string) => {
        try {
            setConnected(await getJSON<ConnectedResource[]>(`/api/k8s/service-routes?${new URLSearchParams({ namespace: ns, service })}`));
        } catch {
            setConnected([]);
        }
    };

    // Reset the form when the modal opens.
    useEffect(() => {
        if (!isOpen) return;
        setResolvedInfo(null);
        setConnected([]);
        setSelectedDepToAdd("");
        if (initialData) {
            setFormData(initialData);
            setTimeoutInput(initialData.idle_timeout ? formatDuration(initialData.idle_timeout) : DEFAULT_TIMEOUT);
            loadDeployments(initialData.namespace);
            if (initialData.target_service) loadConnected(initialData.namespace, initialData.target_service);
        } else {
            const ns = info?.default || namespaces[0] || "";
            setFormData(emptyRoute(ns));
            setTimeoutInput(DEFAULT_TIMEOUT);
            loadDeployments(ns);
        }
        // Only when the modal opens or switches route, not on every poll of the cluster info.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [isOpen, initialData]);

    if (!isOpen) return null;

    const changeNamespace = (ns: string) => {
        setFormData(prev => ({ ...prev, namespace: ns, deployment: "", target_service: "", target_port: 80, dependencies: [] }));
        setResolvedInfo(null);
        setConnected([]);
        loadDeployments(ns);
    };

    const selectDeployment = async (dep: string) => {
        setFormData(prev => ({ ...prev, deployment: dep }));
        setResolvedInfo(null);
        setConnected([]);
        if (!dep) return;
        const ns = formData.namespace || "";
        try {
            const svc = await getJSON<{ service: string; port: number }>(
                `/api/k8s/deployment-service-info?${new URLSearchParams({ namespace: ns, deployment: dep })}`);
            if (svc.service) {
                setFormData(prev => ({ ...prev, target_service: svc.service, target_port: svc.port }));
                setResolvedInfo(`Found Service ${svc.service} on port ${svc.port}.`);
                loadConnected(ns, svc.service);
            } else {
                setFormData(prev => ({ ...prev, target_service: dep, target_port: 80 }));
                setResolvedInfo("No Service selects this deployment; enter the Service and port yourself.");
            }
        } catch {
            setResolvedInfo("Could not look up the deployment's Service.");
        }
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        const idle = parseDuration(timeoutInput);
        if (idle === null) return;
        setSaving(true);
        const ok = await onSubmit({ ...formData, idle_timeout: idle });
        setSaving(false);
        if (ok) onClose();
    };

    const hosts = splitHosts(formData.host || "");
    const toggleHost = (host: string) => {
        const next = hosts.includes(host) ? hosts.filter(h => h !== host) : [...hosts, host];
        setFormData(prev => ({ ...prev, host: next.join(", ") }));
    };

    const moveDependency = (index: number, direction: -1 | 1) => {
        const deps = [...(formData.dependencies || [])];
        const target = index + direction;
        if (target < 0 || target >= deps.length) return;
        [deps[index], deps[target]] = [deps[target], deps[index]];
        setFormData(prev => ({ ...prev, dependencies: deps }));
    };

    const addDependency = () => {
        if (selectedDepToAdd && !formData.dependencies?.some(d => d.name === selectedDepToAdd)) {
            setFormData(prev => ({ ...prev, dependencies: [...(prev.dependencies || []), { name: selectedDepToAdd, stop_on_idle: true }] }));
            setSelectedDepToAdd("");
        }
    };

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm" role="dialog" aria-modal="true">
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-2xl max-h-[90vh] overflow-y-auto">
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-center">
                    <h2 className="text-lg font-bold text-white">{initialData ? "Edit route" : "New route"}</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label="Close"><X className="w-6 h-6" /></button>
                </div>

                <form onSubmit={handleSubmit} className="p-6 space-y-5">
                    {initialData?.declarative && (
                        <div className="bg-purple-900/20 border border-purple-800 text-purple-200 px-4 py-3 rounded-lg text-sm">
                            This route is defined by <span className="font-mono">smart-proxy/*</span> annotations on its Ingress/Route.
                            Changes made here are overwritten within 30 seconds; edit the annotations instead (usually in Git).
                        </div>
                    )}
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-namespace">Namespace</label>
                            <select
                                id="rm-namespace"
                                className={inputClass}
                                value={formData.namespace}
                                onChange={e => changeNamespace(e.target.value)}
                                disabled={boundToResource}
                                title={boundToResource ? "This route belongs to a patched Ingress/Route in this namespace" : undefined}
                                required
                            >
                                {!namespaces.includes(formData.namespace || "") && <option value={formData.namespace}>{formData.namespace || "Select…"}</option>}
                                {namespaces.map(ns => <option key={ns} value={ns}>{ns}</option>)}
                            </select>
                        </div>
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-deployment">Workload</label>
                            <select id="rm-deployment" className={inputClass} value={formData.deployment} onChange={e => selectDeployment(e.target.value)} required>
                                <option value="">Select a workload…</option>
                                {formData.deployment && !deployments.includes(formData.deployment) && <option value={formData.deployment}>{workloadLabel(formData.deployment)}</option>}
                                {deployments.map(d => <option key={d} value={d}>{workloadLabel(d)}</option>)}
                            </select>
                        </div>
                    </div>

                    {resolvedInfo && (
                        <div className="bg-blue-900/20 border border-blue-800 text-blue-200 px-4 py-3 rounded-lg text-sm space-y-2">
                            <p>{resolvedInfo}</p>
                            {connected.length > 0 && (
                                <div>
                                    <p className="text-xs text-blue-300 mb-1.5">Hosts already serving this Service (select to use them):</p>
                                    <ul className="space-y-1">
                                        {connected.map(r => (
                                            <li key={`${r.type}-${r.name}`}>
                                                <label className="flex items-center gap-2 cursor-pointer text-xs">
                                                    <input type="checkbox" checked={hosts.includes(r.host)} onChange={() => toggleHost(r.host)} className="w-4 h-4 rounded bg-gray-900 border-gray-600" />
                                                    <span className="font-mono text-gray-200">{r.host}</span>
                                                    <span className="text-gray-500">
                                                        ({r.type} {r.name}{r.alternate ? `, alternate backend, ${Math.round(r.share)}%` : r.share < 100 ? `, ${Math.round(r.share)}%` : ""})
                                                    </span>
                                                </label>
                                            </li>
                                        ))}
                                    </ul>
                                </div>
                            )}
                        </div>
                    )}

                    <div className="grid grid-cols-1 sm:grid-cols-[2fr_1fr] gap-4">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-host">Hosts</label>
                            <input id="rm-host" type="text" placeholder="app.example.com, www.example.com" className={inputClass}
                                value={formData.host} onChange={e => setFormData({ ...formData, host: e.target.value })} />
                            <p className="text-xs text-gray-500 mt-1">Comma-separated. Empty matches any host.</p>
                        </div>
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-path">Path</label>
                            <input id="rm-path" type="text" className={inputClass} value={formData.path} onChange={e => setFormData({ ...formData, path: e.target.value })} />
                        </div>
                    </div>

                    <div className="grid grid-cols-1 sm:grid-cols-[2fr_1fr] gap-4">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-service">Service</label>
                            <input id="rm-service" type="text" className={inputClass} value={formData.target_service}
                                onChange={e => setFormData({ ...formData, target_service: e.target.value })} required />
                        </div>
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-port">Port</label>
                            <input id="rm-port" type="number" min={1} max={65535} className={inputClass} value={formData.target_port}
                                onChange={e => setFormData({ ...formData, target_port: parseInt(e.target.value) || 0 })} required />
                        </div>
                    </div>

                    <div className="bg-gray-700/30 p-4 rounded-lg border border-gray-700 space-y-3">
                        <div className="flex flex-wrap items-start justify-between gap-2">
                            <div>
                                <p className="text-gray-200 text-sm font-medium">Dependencies</p>
                                <p className="text-xs text-gray-500">
                                    {formData.start_in_order
                                        ? "Woken one at a time in this order, each once the previous is ready; the app starts last."
                                        : "Woken together with the app."}{" "}
                                    "Sleeps too" puts them to sleep when it goes idle.
                                </p>
                            </div>
                            <label className="flex items-center gap-2 text-xs text-gray-300 cursor-pointer shrink-0">
                                <input type="checkbox" className="w-3.5 h-3.5 rounded bg-gray-800 border-gray-500" checked={formData.start_in_order || false}
                                    onChange={e => setFormData({ ...formData, start_in_order: e.target.checked })} />
                                Start in order
                            </label>
                        </div>
                        {(formData.dependencies || []).length === 0 && <div className="text-gray-500 text-sm italic">No dependencies.</div>}
                        {(formData.dependencies || []).map((dep, idx) => (
                            <div key={dep.name} className="flex justify-between items-center gap-2 bg-gray-800 px-3 py-2 rounded border border-gray-600">
                                <span className="flex items-center gap-2 min-w-0">
                                    {formData.start_in_order && <span className="text-gray-500 font-mono text-xs">{idx + 1}.</span>}
                                    <span className="text-white font-mono text-sm truncate">{workloadLabel(dep.name)}</span>
                                </span>
                                <div className="flex items-center gap-3 shrink-0">
                                    <label className="flex items-center gap-1.5 cursor-pointer text-xs text-gray-300">
                                        <input
                                            type="checkbox"
                                            className="w-3.5 h-3.5 rounded bg-gray-800 border-gray-500"
                                            checked={dep.stop_on_idle}
                                            onChange={() => {
                                                const deps = [...(formData.dependencies || [])];
                                                deps[idx] = { ...dep, stop_on_idle: !dep.stop_on_idle };
                                                setFormData({ ...formData, dependencies: deps });
                                            }}
                                        />
                                        Sleeps too
                                    </label>
                                    {formData.start_in_order && (
                                        <span className="flex">
                                            <button type="button" onClick={() => moveDependency(idx, -1)} disabled={idx === 0}
                                                className="p-1 text-gray-400 hover:text-white disabled:opacity-30" aria-label={`Move ${dep.name} up`}><ArrowUp size={14} /></button>
                                            <button type="button" onClick={() => moveDependency(idx, 1)} disabled={idx === (formData.dependencies?.length || 0) - 1}
                                                className="p-1 text-gray-400 hover:text-white disabled:opacity-30" aria-label={`Move ${dep.name} down`}><ArrowDown size={14} /></button>
                                        </span>
                                    )}
                                    <button type="button" onClick={() => setFormData(prev => ({ ...prev, dependencies: prev.dependencies?.filter(d => d.name !== dep.name) }))}
                                        className="text-red-400 hover:text-red-300" aria-label={`Remove ${dep.name}`}>
                                        <X size={16} />
                                    </button>
                                </div>
                            </div>
                        ))}
                        <div className="flex gap-2">
                            <select className="flex-1 bg-gray-700 border border-gray-600 rounded-lg px-3 py-1.5 text-sm text-white focus:outline-none" value={selectedDepToAdd}
                                onChange={e => setSelectedDepToAdd(e.target.value)} aria-label="Dependency to add">
                                <option value="">Add a dependency…</option>
                                {deployments
                                    .filter(d => d !== formData.deployment && !formData.dependencies?.some(dep => dep.name === d))
                                    .map(d => <option key={d} value={d}>{workloadLabel(d)}</option>)}
                            </select>
                            <Button type="button" size="sm" onClick={addDependency} disabled={!selectedDepToAdd} aria-label="Add dependency"><Plus size={16} /></Button>
                        </div>
                    </div>

                    {(formData.backends?.length || 0) > 1 && (
                        <div className="bg-gray-700/30 p-4 rounded-lg border border-gray-700 space-y-2">
                            <p className="text-sm text-gray-200 font-medium">Backends</p>
                            <p className="text-xs text-gray-500">
                                This Route balances traffic across several Services. Managed ones sleep and wake with the app; the others
                                are never touched and get their share only while they run (keep a backend that is off on purpose unmanaged).
                            </p>
                            {formData.backends!.map((b, idx) => (
                                <label key={b.service} className="flex items-center justify-between gap-2 bg-gray-800 px-3 py-2 rounded border border-gray-600 cursor-pointer">
                                    <span className="min-w-0">
                                        <span className="block font-mono text-sm text-white truncate">{b.service}:{b.port}</span>
                                        <span className="block text-xs text-gray-500">weight {b.weight}{b.workload ? ` · ${workloadLabel(b.workload)}` : ""}</span>
                                    </span>
                                    <span className="flex items-center gap-2 text-xs text-gray-300 shrink-0">
                                        <input type="checkbox" className="w-4 h-4 rounded bg-gray-700 border-gray-600" checked={b.managed}
                                            onChange={e => {
                                                const backends = [...formData.backends!];
                                                backends[idx] = { ...b, managed: e.target.checked };
                                                setFormData(prev => ({ ...prev, backends }));
                                            }} />
                                        Sleep &amp; wake with the app
                                    </span>
                                </label>
                            ))}
                        </div>
                    )}

                    <IgnoreEditor
                        rules={formData.ignore || {}}
                        whenAsleep={(formData.when_asleep || "respond") as WhenAsleep}
                        defaults={info?.ignore_defaults}
                        onChange={(ignore, when_asleep) => setFormData(prev => ({ ...prev, ignore, when_asleep }))}
                    />

                    <ScheduleEditor
                        schedule={formData.schedule || null}
                        onChange={schedule => setFormData(prev => ({ ...prev, schedule }))}
                    />

                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 items-start">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1" htmlFor="rm-timeout">Sleep after</label>
                            <input id="rm-timeout" type="text" placeholder={DEFAULT_TIMEOUT}
                                className={`${inputClass} ${timeoutValid ? "" : "border-red-500 focus:ring-red-500"}`}
                                value={timeoutInput} onChange={e => setTimeoutInput(e.target.value)} aria-invalid={!timeoutValid} />
                            <p className={`text-xs mt-1 ${timeoutValid ? "text-gray-500" : "text-red-400"}`}>
                                {timeoutValid ? "Of inactivity, e.g. 30m, 1h, 1h30m." : "Use a duration like 30m, 1h or 90s."}
                            </p>
                        </div>
                        <div className="space-y-2 sm:pt-6">
                            <label className="flex items-center gap-2 text-sm text-white cursor-pointer">
                                <input type="checkbox" className="w-4 h-4 rounded bg-gray-700 border-gray-600" checked={formData.always_on || false}
                                    onChange={e => setFormData({ ...formData, always_on: e.target.checked })} />
                                Always on (never sleep)
                            </label>
                            <label className="flex items-center gap-2 text-sm text-white cursor-pointer">
                                <input type="checkbox" className="w-4 h-4 rounded bg-gray-700 border-gray-600" checked={formData.inject_badge || false}
                                    onChange={e => setFormData({ ...formData, inject_badge: e.target.checked })} />
                                Show a "Powered by Smart Proxy" badge
                            </label>
                        </div>
                    </div>

                    <div className="flex justify-end gap-3 pt-4 border-t border-gray-700">
                        <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
                        <Button type="submit" disabled={saving || !timeoutValid}>{saving ? "Saving…" : "Save route"}</Button>
                    </div>
                </form>
            </div>
        </div>
    );
}

function ScheduleEditor({ schedule, onChange }: { schedule: Schedule | null; onChange: (s: Schedule | null) => void }) {
    const days = schedule?.days || [];
    const toggleDay = (day: string) => {
        if (!schedule) return;
        const next = days.includes(day) ? days.filter(d => d !== day) : [...days, day];
        onChange({ ...schedule, days: next });
    };
    const timeClass = "bg-gray-700 border border-gray-600 rounded-lg px-2 py-1.5 text-sm text-white focus:outline-none focus:ring-2 focus:ring-blue-500";

    return (
        <div className="bg-gray-700/30 p-4 rounded-lg border border-gray-700 space-y-3">
            <label className="flex items-start gap-2 cursor-pointer">
                <input type="checkbox" className="mt-0.5 w-4 h-4 rounded bg-gray-700 border-gray-600" checked={!!schedule}
                    onChange={e => onChange(e.target.checked ? defaultSchedule() : null)} />
                <span>
                    <span className="block text-sm text-gray-200 font-medium">Keep awake on a schedule</span>
                    <span className="block text-xs text-gray-500">Woken at the start and never put to sleep during these hours, e.g. office hours. Outside them the idle timeout applies.</span>
                </span>
            </label>
            {schedule && (
                <div className="space-y-3 pl-6">
                    <div className="flex flex-wrap gap-1" role="group" aria-label="Days">
                        {DAYS.map(d => (
                            <button key={d.value} type="button" onClick={() => toggleDay(d.value)}
                                aria-pressed={days.includes(d.value)}
                                className={`px-2.5 py-1 text-xs rounded-md border transition-colors ${days.includes(d.value) ? "bg-blue-600 border-blue-500 text-white" : "bg-gray-800 border-gray-600 text-gray-400 hover:text-white"}`}>
                                {d.label}
                            </button>
                        ))}
                        <span className="text-xs text-gray-500 self-center ml-1">{days.length === 0 ? "every day" : ""}</span>
                    </div>
                    <div className="flex flex-wrap items-center gap-2 text-sm text-gray-300">
                        <span>From</span>
                        <input type="time" className={timeClass} value={schedule.from} onChange={e => onChange({ ...schedule, from: e.target.value })} aria-label="From" required />
                        <span>to</span>
                        <input type="time" className={timeClass} value={schedule.to} onChange={e => onChange({ ...schedule, to: e.target.value })} aria-label="To" required />
                        <input type="text" className={`${timeClass} flex-1 min-w-[10rem]`} value={schedule.timezone || ""} placeholder="UTC"
                            onChange={e => onChange({ ...schedule, timezone: e.target.value })} aria-label="Timezone" title="IANA timezone, e.g. Europe/Rome" />
                    </div>
                    {schedule.from && schedule.to && schedule.to < schedule.from && (
                        <p className="text-xs text-gray-500">Spans midnight: until {schedule.to} the next day.</p>
                    )}
                </div>
            )}
        </div>
    );
}

const RULE_FIELDS: { key: keyof TrafficRules; label: string; placeholder: string }[] = [
    { key: "user_agents", label: "User agents contain", placeholder: "MyMonitor, internal-checker" },
    { key: "paths", label: "Paths", placeholder: "/healthz, /status/*" },
    { key: "sources", label: "Client IPs / CIDRs", placeholder: "10.20.0.0/16" },
    { key: "methods", label: "Methods", placeholder: "HEAD" },
];

function IgnoreEditor({ rules, whenAsleep, defaults, onChange }: {
    rules: TrafficRules;
    whenAsleep: WhenAsleep;
    defaults?: TrafficRules;
    onChange: (rules: TrafficRules, whenAsleep: WhenAsleep) => void;
}) {
    const split = (v: string) => v.split(/[,\n]/).map(s => s.trim()).filter(Boolean);
    // The text as typed (with spaces and trailing commas); the parsed lists go to the form.
    const [text, setText] = useState<Record<string, string>>(() =>
        Object.fromEntries(RULE_FIELDS.map(f => [f.key, (rules[f.key] || []).join(", ")])));
    const field = "w-full bg-gray-700 border border-gray-600 rounded-lg px-3 py-1.5 text-sm text-white focus:outline-none focus:ring-2 focus:ring-blue-500";

    return (
        <div className="bg-gray-700/30 p-4 rounded-lg border border-gray-700 space-y-3">
            <div>
                <p className="text-sm text-gray-200 font-medium">Monitors &amp; health checks</p>
                <p className="text-xs text-gray-500">
                    Matching requests don&apos;t count as activity and never wake the app. Common monitors are ignored already
                    {defaults?.user_agents?.length ? (
                        <details className="inline">
                            <summary className="inline cursor-pointer text-gray-400 hover:text-gray-200"> (list)</summary>
                            <span className="block mt-1 text-gray-400">{defaults.user_agents.join(", ")}</span>
                        </details>
                    ) : null}
                    .
                </p>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                {RULE_FIELDS.map(f => (
                    <label key={f.key} className="block">
                        <span className="block text-xs text-gray-400 mb-1">{f.label}</span>
                        <input type="text" className={field} placeholder={f.placeholder}
                            value={text[f.key]}
                            onChange={e => {
                                setText({ ...text, [f.key]: e.target.value });
                                onChange({ ...rules, [f.key]: split(e.target.value) }, whenAsleep);
                            }} />
                    </label>
                ))}
            </div>
            <label className="block">
                <span className="block text-xs text-gray-400 mb-1">While the app sleeps, answer them with</span>
                <select className={field} value={whenAsleep} onChange={e => onChange(rules, e.target.value as WhenAsleep)}>
                    <option value="respond">200 OK from Smart Proxy (monitors stay green, the app keeps sleeping)</option>
                    <option value="unavailable">503 Service Unavailable (monitors report it down)</option>
                    <option value="wake">Wake the app (it goes back to sleep after the idle timeout)</option>
                </select>
            </label>
        </div>
    );
}
