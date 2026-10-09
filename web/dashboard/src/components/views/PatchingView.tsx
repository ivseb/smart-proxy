import { useCallback, useEffect, useMemo, useState } from "react";
import { CheckCircle2, Globe, RefreshCw, Route as RouteIcon, Search, Shield, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/Button";
import { StatusDot } from "@/components/ui/StatusBadge";
import { apiRequest, errorMessage } from "@/lib/api";
import { useStoredState } from "@/hooks/useStoredState";
import type { ClusterInfo, PatchableResource } from "@/types/api";

type PatchFilter = "all" | "unpatched" | "patched";

function deploymentStatus(res: PatchableResource): string {
    const d = res.deployment;
    if (!d) return "Unknown";
    if (d.replicas === 0) return "Sleep";
    return d.ready >= d.replicas ? "Ready" : "Scaling";
}

interface PatchingViewProps {
    info: ClusterInfo | null;
    onChanged: () => void;
}

export function PatchingView({ info, onChanged }: PatchingViewProps) {
    const [resources, setResources] = useState<PatchableResource[]>([]);
    const [loading, setLoading] = useState(false);
    const [busy, setBusy] = useState<string | null>(null);
    const [query, setQuery] = useState("");
    const [namespace, setNamespace] = useStoredState("patching.namespace", "");
    const [filter, setFilter] = useStoredState<PatchFilter>("patching.filter", "all");

    const fetchResources = useCallback(async () => {
        setLoading(true);
        try {
            const requests = [apiRequest("/api/k8s/ingresses").then(r => r.json())];
            if (info?.routes_enabled) requests.push(apiRequest("/api/k8s/routes").then(r => r.json()));
            const lists: PatchableResource[][] = await Promise.all(requests);
            setResources(lists.flat());
        } catch (e) {
            toast.error(`Failed to load Ingresses/Routes: ${errorMessage(e)}`);
        } finally {
            setLoading(false);
        }
    }, [info?.routes_enabled]);

    useEffect(() => {
        // Fetch on mount and when Routes become available.
        fetchResources();
    }, [fetchResources]);

    const toggle = async (res: PatchableResource) => {
        const action = res.patched ? "unpatch" : "patch";
        const endpoint = `/api/${action}-${res.type === "Route" ? "route" : "ingress"}`;
        const key = `${res.type}/${res.namespace}/${res.name}`;
        setBusy(key);
        try {
            await apiRequest(`${endpoint}?${new URLSearchParams({ namespace: res.namespace, name: res.name })}`, { method: "POST" });
            toast.success(res.patched
                ? `Restored the original backend of ${res.namespace}/${res.name}`
                : `${res.namespace}/${res.name} now goes through Smart Proxy`);
        } catch (e) {
            toast.error(`Failed to ${action} ${res.type} ${res.name}: ${errorMessage(e)}`);
        } finally {
            setBusy(null);
            onChanged();
            // The cache needs a moment to see the change.
            setTimeout(fetchResources, 500);
        }
    };

    const namespaces = useMemo(() => [...new Set([...(info?.namespaces || []), ...resources.map(r => r.namespace)])].sort(), [info, resources]);
    const activeNamespace = namespaces.includes(namespace) ? namespace : "";

    const visible = useMemo(() => {
        const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
        return resources.filter(r =>
            (!activeNamespace || r.namespace === activeNamespace) &&
            (filter === "all" || (filter === "patched") === r.patched) &&
            terms.every(t => `${r.name} ${r.host} ${r.service} ${r.namespace} ${r.deployment?.name ?? ""}`.toLowerCase().includes(t)));
    }, [resources, activeNamespace, filter, query]);

    const patchedCount = resources.filter(r => r.patched).length;

    return (
        <div className="space-y-4">
            <div>
                <h2 className="text-lg font-semibold text-white">Ingresses &amp; Routes</h2>
                <p className="text-sm text-gray-400">
                    Patch a resource to send its traffic through Smart Proxy: its deployment then sleeps when idle and wakes on the next request.
                </p>
            </div>

            <div className="flex flex-col lg:flex-row lg:items-center gap-3">
                <div className="relative flex-1 min-w-0">
                    <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500" />
                    <input
                        type="search"
                        value={query}
                        onChange={e => setQuery(e.target.value)}
                        placeholder="Search name, host, service…"
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
                    <div className="flex bg-gray-800 border border-gray-700 rounded-lg p-0.5" role="group" aria-label="Patch filter">
                        {(["all", "unpatched", "patched"] as PatchFilter[]).map(f => (
                            <button
                                key={f}
                                onClick={() => setFilter(f)}
                                className={`px-3 py-1.5 text-xs font-medium rounded-md capitalize transition-colors ${filter === f ? "bg-gray-600 text-white" : "text-gray-400 hover:text-white"}`}
                            >
                                {f}
                            </button>
                        ))}
                    </div>
                    <Button variant="ghost" size="icon" onClick={fetchResources} disabled={loading} title="Refresh" aria-label="Refresh">
                        <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
                    </Button>
                </div>
            </div>

            <div className="text-xs text-gray-500">
                {resources.length} resource{resources.length === 1 ? "" : "s"} · {patchedCount} behind Smart Proxy
                {!info?.routes_enabled && info?.connected && " · OpenShift Routes not available on this cluster"}
            </div>

            <div className="bg-gray-800/60 border border-gray-700 rounded-xl overflow-hidden">
                {visible.length === 0 ? (
                    <div className="py-12 text-center text-sm text-gray-500">
                        {loading ? "Loading…" : resources.length === 0 ? "No Ingresses or Routes in the managed namespaces." : "Nothing matches these filters."}
                    </div>
                ) : (
                    <ul className="divide-y divide-gray-700/70">
                        {visible.map(res => {
                            const key = `${res.type}/${res.namespace}/${res.name}`;
                            const Icon = res.type === "Route" ? RouteIcon : Globe;
                            return (
                                <li key={key} className="grid grid-cols-1 md:grid-cols-[minmax(0,2fr)_minmax(0,1.5fr)_auto] gap-x-4 gap-y-2 px-4 py-3 items-center">
                                    <div className="min-w-0 space-y-1">
                                        <div className="flex items-center gap-2 min-w-0">
                                            <Icon size={14} className="shrink-0 text-gray-400" />
                                            <span className="font-medium text-white truncate">{res.name}</span>
                                            <span className="shrink-0 text-[11px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-300">{res.type}</span>
                                            {res.patched && (
                                                <span className="shrink-0 inline-flex items-center gap-1 text-[11px] px-1.5 py-0.5 rounded bg-blue-900/40 text-blue-200">
                                                    <CheckCircle2 size={11} /> via Smart Proxy
                                                </span>
                                            )}
                                        </div>
                                        <div className="text-xs text-gray-400 truncate pl-6">
                                            <span className="font-mono">{res.namespace}</span>
                                            <span className="text-gray-600"> · </span>
                                            <span title={res.host}>{res.host || "any host"}{res.path !== "/" ? res.path : ""}</span>
                                        </div>
                                    </div>
                                    <div className="min-w-0 text-sm pl-6 md:pl-0">
                                        <div className="text-gray-200 truncate">
                                            {res.service}{res.port ? <span className="text-gray-500">:{res.port}</span> : null}
                                        </div>
                                        <div className="flex items-center gap-1.5 text-xs text-gray-400">
                                            <StatusDot status={deploymentStatus(res)} />
                                            {res.deployment ? <>{res.deployment.name} · {res.deployment.ready}/{res.deployment.replicas}</> : "deployment not found"}
                                        </div>
                                    </div>
                                    <div className="flex justify-end pl-6 md:pl-0">
                                        <Button
                                            variant={res.patched ? "secondary" : "primary"}
                                            size="sm"
                                            onClick={() => toggle(res)}
                                            disabled={busy === key}
                                            className="gap-1.5 min-w-[7.5rem]"
                                        >
                                            {res.patched ? <><Undo2 className="w-4 h-4" /> Unpatch</> : <><Shield className="w-4 h-4" /> Patch</>}
                                        </Button>
                                    </div>
                                </li>
                            );
                        })}
                    </ul>
                )}
            </div>
        </div>
    );
}
