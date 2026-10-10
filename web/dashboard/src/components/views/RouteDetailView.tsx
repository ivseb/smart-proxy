import { useMemo, useState } from "react";
import { ArrowLeft, Edit, Globe, Hand, Octagon, Power, Route as RouteIcon, Trash2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { StatusBadge } from "@/components/ui/StatusBadge";
import type { Condition, LogEntry, RouteConfig, RouteStatus, StatsData } from "@/types/api";
import { TrafficSources } from "@/components/views/TrafficSources";
import { RequestsChart } from "@/components/views/RequestsChart";
import { RequestInspector } from "@/components/views/RequestInspector";
import { BackendsCard } from "@/components/views/BackendsCard";
import { BackendsEditor } from "@/components/views/BackendsEditor";
import { ProtectionCard } from "@/components/views/ProtectionCard";
import { formatDuration, formatRelative, formatSchedule, splitHosts, workloadLabel } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import { t } from "@/lib/i18n";

interface RouteDetailViewProps {
    route: RouteStatus;
    stats: StatsData | null;
    logs: LogEntry[];
    onBack: () => void;
    onEdit: (route: RouteStatus) => void;
    onDelete: (route: RouteStatus) => void;
    onStop: (route: RouteStatus) => void;
    onWake: (route: RouteStatus) => void;
    onSave: (route: Partial<RouteConfig>) => Promise<boolean>;
    onChanged: () => void;
}

export function RouteDetailView({ route, stats, logs, onBack, onEdit, onDelete, onStop, onWake, onSave, onChanged }: RouteDetailViewProps) {
    const now = useNow(1000);
    const [editor, setEditor] = useState<{ prefill: Condition | null } | null>(null);
    const hosts = splitHosts(route.host);
    const requests = stats?.RouteStats?.[route.id] || 0;
    const asleep = route.status === "Sleep";

    // Log lines mentioning this route's deployments (logged as "<namespace>/<name>" or by name).
    const routeLogs = useMemo(() => {
        const names = [route.deployment, ...(route.dependencies || []).map(d => d.name)];
        return logs.filter(l => names.some(n => l.message.includes(`${route.namespace}/${n}`) || l.message.includes(` ${n} `)));
    }, [logs, route]);

    const [sleepsBefore, sleepsAfter] = t("Sleeps {when} without traffic").split("{when}");

    return (
        <div className="space-y-6">
            <Button variant="ghost" onClick={onBack} className="gap-2 text-gray-400 hover:text-white -ml-2">
                <ArrowLeft size={16} />
                {t("Back to routes")}
            </Button>

            <header className="flex flex-col lg:flex-row lg:items-start justify-between gap-4">
                <div className="space-y-2 min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                        {hosts.length > 0 ? hosts.map(h => (
                            <span key={h} className="px-2.5 py-1 text-sm bg-gray-800 text-blue-200 border border-blue-900/50 rounded-md font-mono font-semibold break-all">
                                {h}
                            </span>
                        )) : <span className="text-gray-500 italic text-sm">{t("Any host")}</span>}
                        <StatusBadge status={route.status} />
                    </div>
                    <p className="text-sm text-gray-400">
                        {t("Path")} <span className="font-mono text-gray-200">{route.path || "/"}</span>
                        <span className="mx-2 text-gray-600">·</span>
                        {t("Namespace")} <span className="font-mono text-gray-200">{route.namespace}</span>
                    </p>
                </div>
                <div className="flex flex-wrap gap-2">
                    {asleep ? (
                        <Button variant="secondary" onClick={() => onWake(route)} className="gap-2">
                            <Power className="w-4 h-4 text-green-400" /> {t("Wake now")}
                        </Button>
                    ) : (
                        <Button variant="secondary" onClick={() => onStop(route)} className="gap-2">
                            <Octagon className="w-4 h-4 text-orange-400" /> {t("Sleep now")}
                        </Button>
                    )}
                    <Button variant="secondary" onClick={() => onEdit(route)} className="gap-2">
                        <Edit className="w-4 h-4 text-blue-400" /> {t("Edit")}
                    </Button>
                    <Button variant="danger" onClick={() => onDelete(route)} className="gap-2">
                        <Trash2 className="w-4 h-4" /> {t("Delete")}
                    </Button>
                </div>
            </header>

            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
                <Card>
                    <CardHeader><CardTitle className="text-sm text-gray-400 font-medium">{t("Lifecycle")}</CardTitle></CardHeader>
                    <CardContent className="space-y-2">
                        <div className="text-2xl font-semibold tabular-nums">
                            {asleep ? t("0 replicas") : t("{ready}/{total} ready", { ready: route.ready_replicas, total: route.replicas })}
                        </div>
                        <div className="text-sm text-gray-400">
                            {route.always_on ? t("Always on: never put to sleep")
                                : route.schedule_active ? t("Kept awake by its schedule")
                                : !route.source ? t("Manual route: not put to sleep automatically")
                                    : route.sleeps_at ? <>{sleepsBefore}<span className="text-gray-200">{formatRelative(route.sleeps_at, now)}</span>{sleepsAfter}</>
                                        : t("Wakes on the next request")}
                        </div>
                        <div className="text-xs text-gray-500">{t("Idle timeout {duration}", { duration: formatDuration(route.effective_idle_timeout) })}</div>
                        {route.schedule && (
                            <div className="text-xs text-gray-400">
                                {t("Awake")} {formatSchedule(route.schedule)}{route.schedule.timezone ? ` (${route.schedule.timezone})` : " (UTC)"}
                                {route.schedule_active && <span className="text-green-400"> · {t("now")}</span>}
                            </div>
                        )}
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader><CardTitle className="text-sm text-gray-400 font-medium">{t("Target")}</CardTitle></CardHeader>
                    <CardContent className="space-y-2 text-sm">
                        <div><span className="text-gray-500">{t("Workload")}</span> <span className="font-mono text-gray-100">{workloadLabel(route.deployment)}</span></div>
                        <div><span className="text-gray-500">{t("Service")}</span> <span className="font-mono text-gray-100">{route.target_service}:{route.target_port}</span></div>
                        {route.resources.length > 0 ? (
                            <ul className="space-y-1">
                                {route.resources.map(r => (
                                    <li key={`${r.kind}/${r.name}`} className="flex items-center gap-1.5 text-gray-300 min-w-0">
                                        {r.kind === "Route" ? <RouteIcon size={14} className="shrink-0" /> : <Globe size={14} className="shrink-0" />}
                                        <span className="truncate">{r.kind} <span className="font-mono">{r.namespace}/{r.name}</span></span>
                                    </li>
                                ))}
                            </ul>
                        ) : (
                            <div className="flex items-center gap-1.5 text-gray-300">
                                <Hand size={14} /> {route.source ? t("Not patched right now") : t("Configured manually")}
                            </div>
                        )}
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader><CardTitle className="text-sm text-gray-400 font-medium">
                        {t("Dependencies")}{route.start_in_order && (route.dependencies || []).length > 0 ? ` · ${t("start in order")}` : ""}
                    </CardTitle></CardHeader>
                    <CardContent className="space-y-2">
                        {(route.dependencies || []).length === 0 && <span className="text-sm text-gray-500 italic">{t("None")}</span>}
                        {(route.dependencies || []).map(d => (
                            <div key={d.name} className="flex justify-between items-center gap-2 text-sm">
                                <span className="truncate font-mono">{workloadLabel(d.name)}</span>
                                <span className="flex items-center gap-2 shrink-0">
                                    {d.stop_on_idle && <span className="text-[10px] text-gray-400 border border-gray-600 px-1 rounded">{t("sleeps too")}</span>}
                                    <StatusBadge status={route.dependency_status?.[d.name] || "Unknown"} />
                                </span>
                            </div>
                        ))}
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader><CardTitle className="text-sm text-gray-400 font-medium">{t("Requests")}</CardTitle></CardHeader>
                    <CardContent className="space-y-1">
                        <div className="text-3xl font-semibold tabular-nums">{requests}</div>
                        <div className="text-xs text-gray-500">{t("Proxied since Smart Proxy started")}</div>
                        <div className="text-xs text-gray-500">
                            {t("Last activity {when}", { when: route.last_activity ? formatRelative(route.last_activity, now) : t("never") })}
                        </div>
                    </CardContent>
                </Card>
            </div>

            <Card>
                <CardHeader>
                    <CardTitle>{t("Traffic")}</CardTitle>
                    <p className="text-gray-400 text-sm">{t("Requests per second to this route")}</p>
                </CardHeader>
                <CardContent>
                    <RequestsChart routeId={route.id} height={180} />
                </CardContent>
            </Card>

            <BackendsCard route={route} onEdit={() => setEditor({ prefill: null })} />

            <ProtectionCard route={route} onSave={onSave} onChanged={onChanged} />

            <RequestInspector
                route={route}
                onChanged={onChanged}
                headerAction={route.declarative ? undefined : (name, value) => value === "••••••" ? null : (
                    <Button variant="ghost" size="sm" className="text-xs px-2 py-0.5"
                        title={t("Send requests whose {name} is {value} to a backend of your choice", { name, value })}
                        onClick={() => setEditor({ prefill: { field: "header", name, op: "equals", value } })}>
                        {t("Route like this…")}
                    </Button>
                )}
            />

            <TrafficSources route={route} onSave={onSave} />

            {editor && <BackendsEditor route={route} prefill={editor.prefill} onClose={() => setEditor(null)} onSave={onSave} />}

            <Card>
                <CardHeader><CardTitle>{t("Events")}</CardTitle></CardHeader>
                <CardContent className="max-h-[320px] overflow-y-auto bg-gray-950 font-mono text-xs sm:text-sm">
                    {routeLogs.length === 0 ? (
                        <div className="text-gray-500 italic text-center py-8">{t("No recent events for this route.")}</div>
                    ) : (
                        routeLogs.map((l, i) => (
                            <div key={i} className="py-1 border-b border-gray-900 last:border-0">
                                <span className="text-gray-500 mr-3">{new Date(l.timestamp).toLocaleTimeString()}</span>
                                <span className="text-gray-300 break-words">{l.message}</span>
                            </div>
                        ))
                    )}
                </CardContent>
            </Card>
        </div>
    );
}
