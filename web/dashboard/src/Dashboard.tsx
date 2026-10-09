import { useEffect, useState } from "react";
import { Activity as ActivityIcon, Layers, LayoutDashboard, LogOut, ScrollText, Shield, User } from "lucide-react";
import { Toaster, toast } from "sonner";
import { usePolling } from "@/hooks/usePolling";
import { useAuth } from "@/hooks/useAuth";
import { useStoredState } from "@/hooks/useStoredState";
import type { ClusterInfo, LogEntry, RouteConfig, RouteStatus, StatsData } from "@/types/api";
import { RoutesView } from "@/components/views/RoutesView";
import { LogsView } from "@/components/views/LogsView";
import { PatchingView } from "@/components/views/PatchingView";
import { RouteModal } from "@/components/views/RouteModal";
import { RouteDetailView } from "@/components/views/RouteDetailView";
import { StatsView } from "@/components/views/StatsView";
import { Button } from "@/components/ui/Button";
import { apiRequest, errorMessage } from "@/lib/api";

type Tab = "stats" | "routes" | "patching" | "logs";

// Tabs can be linked to: #overview, #routes, #patching, #logs.
const TAB_HASH: Record<Tab, string> = { stats: "overview", routes: "routes", patching: "patching", logs: "logs" };

function tabFromHash(): Tab | null {
    const hash = window.location.hash.replace(/^#/, "");
    const entry = Object.entries(TAB_HASH).find(([, h]) => h === hash);
    return entry ? (entry[0] as Tab) : null;
}

const query = (params: Record<string, string>) => new URLSearchParams(params).toString();

export function Dashboard() {
    const [storedTab, setActiveTab] = useStoredState<Tab>("dashboard.tab", "routes");
    const [hashTab, setHashTab] = useState<Tab | null>(tabFromHash);
    const activeTab = hashTab ?? storedTab;
    useEffect(() => {
        const onHashChange = () => setHashTab(tabFromHash());
        window.addEventListener("hashchange", onHashChange);
        return () => window.removeEventListener("hashchange", onHashChange);
    }, []);
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingRoute, setEditingRoute] = useState<RouteConfig | null>(null);
    const [selectedRouteId, setSelectedRouteId] = useState<string | null>(null);
    const [routeToDelete, setRouteToDelete] = useState<RouteStatus | null>(null);

    const { data: routes, refetch } = usePolling<RouteStatus[]>("/api/routes", 2000);
    const { data: stats, fetchedAt: statsFetchedAt } = usePolling<StatsData>("/api/stats", 2000);
    // Namespaces can come and go (label selector), so refresh the cluster info now and then.
    const { data: info } = usePolling<ClusterInfo>("/api/info", 30000);
    const auth = useAuth();

    // Recent log lines, for the route detail view.
    const [logs, setLogs] = useState<LogEntry[]>([]);
    useEffect(() => {
        const es = new EventSource("/api/logs");
        es.onmessage = (e) => {
            const entry = JSON.parse(e.data);
            setLogs(prev => [...prev.slice(-199), entry]);
        };
        return () => es.close();
    }, []);

    const run = async (action: () => Promise<unknown>, success: string | null, failure: string) => {
        try {
            await action();
            if (success) toast.success(success);
            return true;
        } catch (e) {
            toast.error(`${failure}: ${errorMessage(e)}`);
            return false;
        } finally {
            refetch();
        }
    };

    const saveRoute = (data: Partial<RouteConfig>) =>
        run(() => apiRequest("/api/routes", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(data),
        }), "Route saved", "Failed to save route");

    // Deleting a route restores every Ingress/Route patched for it (server-side).
    const requestDelete = (route: RouteStatus) => {
        if (route.declarative) {
            toast.error("This route is defined by smart-proxy/* annotations on its Ingress/Route: set smart-proxy/enabled to \"false\" there to remove it.");
            return;
        }
        if (route.resources.length > 0) {
            setRouteToDelete(route);
        } else if (confirm(`Delete the route for ${route.host || route.deployment}?`)) {
            deleteRoute(route);
        }
    };

    const deleteRoute = async (route: RouteStatus) => {
        let restored = 0;
        const ok = await run(async () => {
            const res = await apiRequest(`/api/routes?${query({ id: route.id })}`, { method: "DELETE" });
            restored = ((await res.json()).restored || []).length;
        }, null, "Failed to delete route");
        if (ok) {
            toast.success(restored > 0 ? `Route deleted, ${restored} resource${restored === 1 ? "" : "s"} restored` : "Route deleted");
            setSelectedRouteId(null);
        }
        setRouteToDelete(null);
    };

    const stopRoute = async (route: RouteStatus) => {
        if (!confirm(`Put ${route.namespace}/${route.deployment} to sleep now?`)) return;
        await run(
            () => apiRequest(`/api/k8s/stop-deployment?${query({ namespace: route.namespace, deployment: route.deployment })}`, { method: "POST" }),
            `${route.deployment} is going to sleep`,
            `Failed to stop ${route.deployment}`,
        );
    };

    const wakeRoute = (route: RouteStatus) =>
        run(
            () => apiRequest(`/api/k8s/wake-deployment?${query({ namespace: route.namespace, deployment: route.deployment })}`, { method: "POST" }),
            `Waking up ${route.deployment}`,
            `Failed to wake ${route.deployment}`,
        );

    const openNewModal = () => {
        setEditingRoute(null);
        setIsModalOpen(true);
    };

    const openEditModal = (route: RouteStatus) => {
        setEditingRoute(route);
        setIsModalOpen(true);
    };

    const goTo = (tab: Tab) => {
        setActiveTab(tab);
        setHashTab(tab);
        window.history.replaceState(null, "", `#${TAB_HASH[tab]}`);
        setSelectedRouteId(null);
    };

    const selectedRoute = routes?.find(r => r.id === selectedRouteId);
    const namespaceCount = info?.namespaces.length ?? 0;

    return (
        <div className="min-h-screen bg-gray-950 text-white font-sans selection:bg-blue-500/30">
            <div className="mx-auto max-w-7xl px-4 sm:px-6 py-4 sm:py-6">
                <header className="flex flex-wrap items-center justify-between gap-4 pb-5 border-b border-gray-800">
                    <div className="flex items-center gap-3 min-w-0">
                        <div className="w-10 h-10 shrink-0 bg-blue-600 rounded-lg flex items-center justify-center shadow-lg shadow-blue-500/20">
                            <Shield className="text-white w-6 h-6" />
                        </div>
                        <div className="min-w-0">
                            <h1 className="text-xl sm:text-2xl font-bold tracking-tight">Smart Proxy</h1>
                            {info && (
                                <p className="text-gray-400 text-xs sm:text-sm flex items-center gap-1.5 truncate" title={info.scope}>
                                    <Layers size={14} className="shrink-0" />
                                    {!info.connected
                                        ? "Not connected to a cluster"
                                        : info.all_namespaces
                                            ? `${info.scope} (${namespaceCount})`
                                            : namespaceCount === 1 ? `Namespace ${info.namespaces[0]}` : `${namespaceCount} namespaces`}
                                </p>
                            )}
                        </div>
                    </div>

                    <nav className="order-last w-full md:order-none md:w-auto flex gap-1 bg-gray-800 p-1 rounded-lg border border-gray-700 overflow-x-auto">
                        <TabButton active={activeTab === "stats" && !selectedRouteId} onClick={() => goTo("stats")} icon={<ActivityIcon size={18} />} label="Overview" />
                        <TabButton active={activeTab === "routes" || !!selectedRouteId} onClick={() => goTo("routes")} icon={<LayoutDashboard size={18} />} label="Routes" />
                        <TabButton active={activeTab === "patching" && !selectedRouteId} onClick={() => goTo("patching")} icon={<Shield size={18} />} label="Patching" />
                        <TabButton active={activeTab === "logs" && !selectedRouteId} onClick={() => goTo("logs")} icon={<ScrollText size={18} />} label="Logs" />
                    </nav>

                    {auth && auth.mode !== "none" && (
                        <div className="flex items-center gap-2 text-sm text-gray-400">
                            {auth.user && (
                                <span className="flex items-center gap-1.5 max-w-[12rem] truncate" title={`Signed in (${auth.mode})`}>
                                    <User size={16} className="shrink-0" />
                                    <span className="truncate">{auth.user}</span>
                                </span>
                            )}
                            {auth.logout_url && (
                                <a href={auth.logout_url} className="flex items-center gap-1.5 px-2 py-1 rounded-md hover:text-white hover:bg-white/5 transition-colors">
                                    <LogOut size={16} />
                                    <span className="hidden sm:inline">Sign out</span>
                                </a>
                            )}
                        </div>
                    )}
                </header>

                <main className="pt-6">
                    {selectedRouteId && selectedRoute ? (
                        <RouteDetailView
                            route={selectedRoute}
                            stats={stats}
                            logs={logs}
                            onBack={() => setSelectedRouteId(null)}
                            onEdit={openEditModal}
                            onDelete={requestDelete}
                            onStop={stopRoute}
                            onWake={wakeRoute}
                        />
                    ) : (
                        <>
                            {activeTab === "stats" && <StatsView stats={stats} fetchedAt={statsFetchedAt} routes={routes || []} info={info} />}
                            {activeTab === "routes" && (
                                <RoutesView
                                    routes={routes || []}
                                    stats={stats}
                                    info={info}
                                    onNew={openNewModal}
                                    onEdit={openEditModal}
                                    onDelete={requestDelete}
                                    onStop={stopRoute}
                                    onWake={wakeRoute}
                                    onSelect={setSelectedRouteId}
                                />
                            )}
                            {activeTab === "patching" && <PatchingView info={info} onChanged={refetch} />}
                            {activeTab === "logs" && <LogsView replica={info?.replica} />}
                        </>
                    )}
                </main>
            </div>

            <RouteModal
                isOpen={isModalOpen}
                onClose={() => setIsModalOpen(false)}
                onSubmit={saveRoute}
                initialData={editingRoute}
                info={info}
            />

            {routeToDelete && (
                <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm" role="dialog" aria-modal="true">
                    <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-md p-6">
                        <h3 className="text-xl font-bold text-white mb-2">Delete route?</h3>
                        <p className="text-gray-400 mb-3 text-sm">
                            These resources point at Smart Proxy for this route and get their original backend back:
                        </p>
                        <ul className="mb-6 space-y-1 text-sm">
                            {routeToDelete.resources.map(r => (
                                <li key={`${r.kind}/${r.namespace}/${r.name}`} className="font-mono text-gray-200 truncate">
                                    {r.kind} {r.namespace}/{r.name} <span className="text-gray-500">({r.host || "any host"})</span>
                                </li>
                            ))}
                        </ul>
                        <div className="flex flex-col gap-3">
                            <Button variant="danger" onClick={() => deleteRoute(routeToDelete)} className="w-full">
                                Delete and restore {routeToDelete.resources.length === 1 ? routeToDelete.resources[0].kind : `${routeToDelete.resources.length} resources`}
                            </Button>
                            <Button variant="secondary" onClick={() => setRouteToDelete(null)} className="w-full">
                                Cancel
                            </Button>
                        </div>
                    </div>
                </div>
            )}

            <Toaster position="top-right" theme="dark" />
        </div>
    );
}

function TabButton({ active, onClick, icon, label }: { active: boolean; onClick: () => void; icon: React.ReactNode; label: string }) {
    return (
        <button
            onClick={onClick}
            className={`flex items-center gap-2 px-3 sm:px-4 py-2 rounded-md text-sm font-medium whitespace-nowrap transition-all ${active
                ? "bg-blue-600 text-white shadow-md"
                : "text-gray-400 hover:text-white hover:bg-gray-700"
                }`}
        >
            {icon}
            <span>{label}</span>
        </button>
    );
}
