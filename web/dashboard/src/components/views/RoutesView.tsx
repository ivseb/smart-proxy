import { useMemo, useState } from "react";
import { CalendarClock, ChevronDown, ChevronRight, Edit2, Globe, Hand, Octagon, Pin, Plus, Power, Route as RouteIcon, Search, Trash2 } from "lucide-react";
import type { ClusterInfo, RouteStatus, StatsData } from "@/types/api";
import { Button } from "@/components/ui/Button";
import { StatusBadge, StatusDot } from "@/components/ui/StatusBadge";
import { formatDuration, formatRelative, formatSchedule, splitHosts, workloadLabel } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import { useStoredState } from "@/hooks/useStoredState";

type StatusFilter = "all" | "awake" | "asleep" | "issues";

const STATUS_FILTERS: { value: StatusFilter; label: string }[] = [
    { value: "all", label: "All" },
    { value: "awake", label: "Awake" },
    { value: "asleep", label: "Asleep" },
    { value: "issues", label: "Issues" },
];

function matchesStatus(route: RouteStatus, filter: StatusFilter): boolean {
    switch (filter) {
        case "awake":
            return route.status === "Ready" || route.status === "Scaling";
        case "asleep":
            return route.status === "Sleep";
        case "issues":
            return route.status === "Error" || route.status === "Unwatched" ||
                Object.values(route.dependency_status || {}).some(s => s === "Error");
        default:
            return true;
    }
}

function matchesSearch(route: RouteStatus, query: string): boolean {
    if (!query) return true;
    const haystack = [
        route.host, route.path, route.deployment, route.target_service, route.namespace,
        route.source?.name ?? "", ...(route.dependencies || []).map(d => d.name),
    ].join(" ").toLowerCase();
    return query.toLowerCase().split(/\s+/).every(term => haystack.includes(term));
}

interface RoutesViewProps {
    routes: RouteStatus[];
    stats: StatsData | null;
    info: ClusterInfo | null;
    onNew: () => void;
    onEdit: (route: RouteStatus) => void;
    onDelete: (route: RouteStatus) => void;
    onStop: (route: RouteStatus) => void;
    onWake: (route: RouteStatus) => void;
    onSelect: (id: string) => void;
}

export function RoutesView({ routes, stats, info, onNew, onEdit, onDelete, onStop, onWake, onSelect }: RoutesViewProps) {
    const [query, setQuery] = useState("");
    const [namespace, setNamespace] = useStoredState("routes.namespace", "");
    const [statusFilter, setStatusFilter] = useStoredState<StatusFilter>("routes.status", "all");
    const [collapsed, setCollapsed] = useStoredState<string[]>("routes.collapsed", []);
    const now = useNow();

    const namespaces = useMemo(() => {
        const set = new Set([...(info?.namespaces || []), ...routes.map(r => r.namespace)]);
        return [...set].sort();
    }, [info, routes]);
    const activeNamespace = namespaces.includes(namespace) ? namespace : "";

    const filtered = useMemo(
        () => routes.filter(r =>
            (!activeNamespace || r.namespace === activeNamespace) &&
            matchesStatus(r, statusFilter) &&
            matchesSearch(r, query)),
        [routes, activeNamespace, statusFilter, query],
    );

    const groups = useMemo(() => {
        const byNs = new Map<string, RouteStatus[]>();
        for (const r of filtered) {
            byNs.set(r.namespace, [...(byNs.get(r.namespace) || []), r]);
        }
        return [...byNs.entries()].sort(([a], [b]) => a.localeCompare(b));
    }, [filtered]);

    const awake = routes.filter(r => r.status === "Ready" || r.status === "Scaling").length;
    const asleep = routes.filter(r => r.status === "Sleep").length;
    // Group headers only help when several namespaces are involved.
    const grouped = namespaces.length > 1 && !activeNamespace;

    const toggleGroup = (ns: string) =>
        setCollapsed(collapsed.includes(ns) ? collapsed.filter(n => n !== ns) : [...collapsed, ns]);

    return (
        <div className="space-y-4">
            {/* Toolbar */}
            <div className="flex flex-col lg:flex-row lg:items-center gap-3">
                <div className="relative flex-1 min-w-0">
                    <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500" />
                    <input
                        type="search"
                        value={query}
                        onChange={e => setQuery(e.target.value)}
                        placeholder="Search host, deployment, service…"
                        className="w-full bg-gray-800 border border-gray-700 rounded-lg pl-9 pr-3 py-2 text-sm text-white placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500/50"
                    />
                </div>
                <div className="flex flex-wrap items-center gap-2">
                    {namespaces.length > 1 && (
                        <select
                            value={activeNamespace}
                            onChange={e => setNamespace(e.target.value)}
                            className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-white focus:outline-none focus:ring-2 focus:ring-blue-500/50"
                            aria-label="Namespace"
                        >
                            <option value="">All namespaces ({namespaces.length})</option>
                            {namespaces.map(ns => <option key={ns} value={ns}>{ns}</option>)}
                        </select>
                    )}
                    <div className="flex bg-gray-800 border border-gray-700 rounded-lg p-0.5" role="group" aria-label="Status filter">
                        {STATUS_FILTERS.map(f => (
                            <button
                                key={f.value}
                                onClick={() => setStatusFilter(f.value)}
                                className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${statusFilter === f.value ? "bg-gray-600 text-white" : "text-gray-400 hover:text-white"}`}
                            >
                                {f.label}
                            </button>
                        ))}
                    </div>
                    <Button onClick={onNew} size="md" className="gap-1.5">
                        <Plus size={16} />
                        New route
                    </Button>
                </div>
            </div>

            <div className="text-xs text-gray-500">
                {routes.length} route{routes.length === 1 ? "" : "s"} · {awake} awake · {asleep} asleep
                {filtered.length !== routes.length && <> · showing {filtered.length}</>}
            </div>

            {routes.length === 0 ? (
                <EmptyState onNew={onNew} />
            ) : filtered.length === 0 ? (
                <div className="py-12 text-center text-sm text-gray-500 border border-dashed border-gray-700 rounded-xl">
                    No routes match these filters.
                </div>
            ) : (
                <div className="space-y-4">
                    {groups.map(([ns, items]) => {
                        const isCollapsed = grouped && collapsed.includes(ns);
                        const groupAwake = items.filter(r => r.status === "Ready" || r.status === "Scaling").length;
                        return (
                            <section key={ns} className="bg-gray-800/60 border border-gray-700 rounded-xl overflow-hidden">
                                {grouped && (
                                    <button
                                        onClick={() => toggleGroup(ns)}
                                        className="w-full flex items-center justify-between px-4 py-2.5 bg-gray-800 hover:bg-gray-700/60 border-b border-gray-700 text-left"
                                        aria-expanded={!isCollapsed}
                                    >
                                        <span className="flex items-center gap-2 font-medium text-white">
                                            {isCollapsed ? <ChevronRight size={16} /> : <ChevronDown size={16} />}
                                            <span className="font-mono text-sm">{ns}</span>
                                        </span>
                                        <span className="text-xs text-gray-400">
                                            {items.length} route{items.length === 1 ? "" : "s"} · {groupAwake} awake
                                        </span>
                                    </button>
                                )}
                                {!isCollapsed && (
                                    <ul className="divide-y divide-gray-700/70">
                                        {items.map(route => (
                                            <RouteRow
                                                key={route.id}
                                                route={route}
                                                requests={stats?.RouteStats?.[route.id] || 0}
                                                now={now}
                                                showNamespace={!grouped && namespaces.length > 1}
                                                onSelect={() => onSelect(route.id)}
                                                onEdit={() => onEdit(route)}
                                                onDelete={() => onDelete(route)}
                                                onStop={() => onStop(route)}
                                                onWake={() => onWake(route)}
                                            />
                                        ))}
                                    </ul>
                                )}
                            </section>
                        );
                    })}
                </div>
            )}
        </div>
    );
}

interface RouteRowProps {
    route: RouteStatus;
    requests: number;
    now: number;
    showNamespace: boolean;
    onSelect: () => void;
    onEdit: () => void;
    onDelete: () => void;
    onStop: () => void;
    onWake: () => void;
}

function RouteRow({ route, requests, now, showNamespace, onSelect, onEdit, onDelete, onStop, onWake }: RouteRowProps) {
    const hosts = splitHosts(route.host);
    const asleep = route.status === "Sleep";
    const deps = route.dependencies || [];

    return (
        <li
            className="grid grid-cols-1 md:grid-cols-[minmax(0,2.2fr)_minmax(0,1.6fr)_minmax(0,1.1fr)_auto] gap-x-4 gap-y-2 px-4 py-3 hover:bg-gray-700/30 cursor-pointer transition-colors"
            onClick={onSelect}
        >
            {/* Where traffic comes in */}
            <div className="min-w-0 space-y-1">
                <div className="flex items-center gap-2 min-w-0">
                    <StatusDot status={route.status} />
                    <span className="font-medium text-white truncate" title={hosts.join(", ")}>
                        {hosts[0] || <span className="italic text-gray-500">any host</span>}
                    </span>
                    {route.path && route.path !== "/" && <span className="font-mono text-sm text-blue-300 truncate">{route.path}</span>}
                    {hosts.length > 1 && (
                        <span className="shrink-0 px-1.5 py-0.5 text-[11px] rounded bg-gray-700 text-gray-300" title={hosts.slice(1).join("\n")}>
                            +{hosts.length - 1}
                        </span>
                    )}
                </div>
                <div className="flex items-center gap-2 text-xs text-gray-400 pl-4 min-w-0">
                    <SourceChip route={route} />
                    {showNamespace && <span className="font-mono truncate">{route.namespace}</span>}
                </div>
            </div>

            {/* What it reaches */}
            <div className="min-w-0 space-y-1 pl-4 md:pl-0">
                <div className="text-sm text-gray-200 truncate" title={`${route.target_service}:${route.target_port}`}>
                    {workloadLabel(route.deployment)}
                    <span className="text-gray-500"> → {route.target_service}:{route.target_port}</span>
                </div>
                {deps.length > 0 && (
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-400">
                        {deps.map(dep => (
                            <span key={dep.name} className="inline-flex items-center gap-1.5" title={`${dep.name}: ${route.dependency_status?.[dep.name] || "Unknown"}`}>
                                <StatusDot status={route.dependency_status?.[dep.name] || "Unknown"} />
                                {workloadLabel(dep.name)}
                            </span>
                        ))}
                    </div>
                )}
            </div>

            {/* Lifecycle */}
            <div className="space-y-1 pl-4 md:pl-0 text-xs">
                <div className="flex items-center gap-2">
                    <StatusBadge status={route.status} />
                    {!asleep && route.replicas > 0 && (
                        <span className="text-gray-400 tabular-nums">{route.ready_replicas}/{route.replicas}</span>
                    )}
                </div>
                <div className="text-gray-500">
                    {route.always_on ? (
                        <span className="inline-flex items-center gap-1"><Pin size={12} /> Always on</span>
                    ) : route.schedule_active && route.schedule ? (
                        <span className="inline-flex items-center gap-1" title="Kept awake by its schedule">
                            <CalendarClock size={12} /> Scheduled until {route.schedule.to}
                        </span>
                    ) : !route.source ? (
                        <span>Not managed by idle timer</span>
                    ) : route.sleeps_at ? (
                        <span title={`Idle timeout ${formatDuration(route.effective_idle_timeout)}`}>
                            Sleeps {formatRelative(route.sleeps_at, now)}
                        </span>
                    ) : (
                        <span>Wakes on next request</span>
                    )}
                    <span className="text-gray-600"> · {requests} req</span>
                    {route.schedule && !route.schedule_active && (
                        <div className="inline-flex items-center gap-1 text-gray-500" title={`Kept awake ${formatSchedule(route.schedule)}${route.schedule.timezone ? ` (${route.schedule.timezone})` : ""}`}>
                            <CalendarClock size={12} /> {formatSchedule(route.schedule)}
                        </div>
                    )}
                </div>
            </div>

            {/* Actions */}
            <div className="flex items-center justify-end gap-1 pl-4 md:pl-0" onClick={e => e.stopPropagation()}>
                {asleep ? (
                    <Button variant="ghost" size="icon" onClick={onWake} title="Wake now" aria-label="Wake now">
                        <Power className="w-4 h-4 text-green-400" />
                    </Button>
                ) : (
                    <Button variant="ghost" size="icon" onClick={onStop} title="Put to sleep now" aria-label="Put to sleep now">
                        <Octagon className="w-4 h-4 text-orange-400" />
                    </Button>
                )}
                <Button variant="ghost" size="icon" onClick={onEdit} title="Edit" aria-label="Edit">
                    <Edit2 className="w-4 h-4 text-blue-400" />
                </Button>
                <Button variant="ghost" size="icon" onClick={onDelete} title="Delete" aria-label="Delete">
                    <Trash2 className="w-4 h-4 text-gray-400" />
                </Button>
            </div>
        </li>
    );
}

function SourceChip({ route }: { route: RouteStatus }) {
    if (!route.source) {
        return (
            <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-gray-700/60 text-gray-300" title="Configured manually; traffic must be routed to Smart Proxy by you">
                <Hand size={11} /> Manual
            </span>
        );
    }
    const Icon = route.source.kind === "Route" ? RouteIcon : Globe;
    return (
        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-blue-900/30 text-blue-200 min-w-0" title={`${route.source.kind} ${route.source.namespace}/${route.source.name}`}>
            <Icon size={11} className="shrink-0" />
            <span className="truncate">{route.source.kind} {route.source.name}</span>
        </span>
    );
}

function EmptyState({ onNew }: { onNew: () => void }) {
    return (
        <div className="py-16 text-center border border-dashed border-gray-700 rounded-xl space-y-3">
            <p className="text-gray-300 font-medium">No routes yet</p>
            <p className="text-sm text-gray-500 max-w-md mx-auto">
                Patch an Ingress or Route from the <span className="text-gray-300">Patching</span> tab, or create a route manually.
            </p>
            <Button onClick={onNew} className="gap-1.5"><Plus size={16} /> New route</Button>
        </div>
    );
}
