import { useState, useEffect } from "react";
import { usePolling } from "@/hooks/usePolling";
import type { RouteConfig, RouteStatus, StatsData } from "@/types/api";
import { RouteTable } from "@/components/views/RouteTable";
import { LogsView } from "@/components/views/LogsView";
import { PatchingView } from "@/components/views/PatchingView";
import { RouteModal } from "@/components/views/RouteModal";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Plus, LayoutDashboard, ScrollText, Shield, Activity as ActivityIcon } from "lucide-react";
import { StatsView } from "@/components/views/StatsView";
import { Toaster } from "sonner";

import { RouteDetailView } from "@/components/views/RouteDetailView";
import type { LogEntry } from "@/types/api";

export function Dashboard() {
    const [activeTab, setActiveTab] = useState<"stats" | "routes" | "patching" | "logs">("stats");
    const [isModalOpen, setIsModalOpen] = useState(false);
    const [editingRoute, setEditingRoute] = useState<RouteConfig | null>(null);
    const [selectedRouteId, setSelectedRouteId] = useState<string | null>(null);

    const { data: routes, refetch } = usePolling<RouteStatus[]>("/api/routes", 2000);
    const { data: stats } = usePolling<StatsData>("/api/stats", 2000);
    // Mock logs for now or pull from LogsView context if we lift state.
    // For MVP, let's just pass empty array or fetch logs if needed.
    // Actually, LogsView fetches its own logs. We should lift logs state or fetch here.
    const [logs, setLogs] = useState<LogEntry[]>([]);

    // Fetch logs separately just to feed detail view
    useEffect(() => {
        const es = new EventSource('/api/logs');
        es.onmessage = (e) => {
            const entry = JSON.parse(e.data);
            setLogs(prev => [...prev.slice(-99), entry]); // Keep last 100
        };
        return () => es.close();
    }, []);

    const handleCreateRoute = async (data: Partial<RouteConfig>) => {
        await fetch("/api/routes", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(data),
        });
        refetch();
    };

    const [routeToDelete, setRouteToDelete] = useState<string | null>(null);

    const handleDeleteClick = (id: string) => {
        if (id.startsWith("ing-")) {
            setRouteToDelete(id);
        } else {
            if (confirm("Are you sure you want to delete this route?")) {
                deleteRoute(id);
            }
        }
    };

    const deleteRoute = async (id: string) => {
        await fetch(`/api/routes?id=${id}`, { method: "DELETE" });
        setSelectedRouteId(null);
        setRouteToDelete(null);
        refetch();
    };

    const handleUnpatch = async (id: string) => {
        const name = id.replace("ing-", "");
        await fetch(`/api/ingress/unpatch?name=${name}`, { method: "POST" });
        setSelectedRouteId(null);
        setRouteToDelete(null);
        refetch();
    };

    const handleStopDeployment = async (route: RouteStatus) => {
        if (!confirm(`Stop deployment ${route.deployment}?`)) return;
        await fetch(`/api/k8s/stop-deployment?namespace=${route.namespace}&deployment=${route.deployment}`, {
            method: "POST"
        });
        refetch();
    };

    const openNewModal = () => {
        setEditingRoute(null);
        setIsModalOpen(true);
    };

    const openEditModal = (route: RouteStatus) => {
        setEditingRoute(route);
        setIsModalOpen(true);
    };

    const selectedRoute = routes?.find(r => r.id === selectedRouteId);

    return (
        <div className="min-h-screen bg-gray-950 text-white font-sans selection:bg-blue-500/30">
            <div className="container mx-auto p-6 max-w-7xl">
                {/* Header hidden in Detail View for cleaner look, or keep? Keep. */}
                <header className="flex justify-between items-center pb-6 border-b border-gray-800">
                    <div className="flex items-center space-x-4">
                        <div className="w-10 h-10 bg-blue-600 rounded-lg flex items-center justify-center shadow-lg shadow-blue-500/20">
                            <Shield className="text-white w-6 h-6" />
                        </div>
                        <div>
                            <h1 className="text-2xl font-bold tracking-tight">Smart Proxy Admin</h1>
                            <p className="text-gray-400 text-sm">Cluster Management & Ingress Patching</p>
                        </div>
                    </div>

                    <nav className="flex space-x-1 bg-gray-800 p-1 rounded-lg border border-gray-700">
                        <TabButton
                            active={activeTab === "stats" && !selectedRouteId}
                            onClick={() => { setActiveTab("stats"); setSelectedRouteId(null); }}
                            icon={<ActivityIcon size={18} />}
                            label="Overview"
                        />
                        <TabButton
                            active={activeTab === "routes" || !!selectedRouteId}
                            onClick={() => setActiveTab("routes")}
                            icon={<LayoutDashboard size={18} />}
                            label="Routes"
                        />
                        <TabButton
                            active={activeTab === "patching"}
                            onClick={() => { setActiveTab("patching"); setSelectedRouteId(null); }}
                            icon={<Shield size={18} />}
                            label="Patching"
                        />
                        <TabButton
                            active={activeTab === "logs"}
                            onClick={() => { setActiveTab("logs"); setSelectedRouteId(null); }}
                            icon={<ScrollText size={18} />}
                            label="Logs"
                        />
                    </nav>
                </header>

                <main>
                    {activeTab === "stats" && !selectedRouteId && <StatsView stats={stats} />}

                    {activeTab === "routes" && !selectedRouteId && (
                        <Card>
                            <CardHeader className="flex flex-row items-center justify-between">
                                <CardTitle>Configured Routes</CardTitle>
                                <Button onClick={openNewModal} size="sm" className="space-x-2">
                                    <Plus size={16} />
                                    <span>New Route</span>
                                </Button>
                            </CardHeader>
                            <RouteTable
                                routes={routes || []}
                                stats={stats}
                                onEdit={openEditModal}
                                onDelete={handleDeleteClick}
                                onStop={handleStopDeployment}
                                onSelect={(id) => setSelectedRouteId(id)}
                            />
                        </Card>
                    )}

                    {selectedRouteId && selectedRoute && (
                        <div className="bg-gray-800/50 p-6 rounded-2xl border border-gray-700">
                            <RouteDetailView
                                route={selectedRoute}
                                stats={stats}
                                logs={logs}
                                onBack={() => setSelectedRouteId(null)}
                                onEdit={openEditModal}
                                onDelete={handleDeleteClick}
                                onStop={handleStopDeployment}
                            />
                        </div>
                    )}

                    {activeTab === "patching" && !selectedRouteId && (
                        <Card>
                            <CardHeader>
                                <CardTitle>Ingress Management</CardTitle>
                                <p className="text-sm text-gray-400">Scan namespace for Ingresses and patch them to route through Smart Proxy.</p>
                            </CardHeader>
                            <CardContent>
                                <PatchingView />
                            </CardContent>
                        </Card>
                    )}

                    {activeTab === "logs" && !selectedRouteId && (
                        <LogsView />
                    )}
                </main>
            </div>

            <RouteModal
                isOpen={isModalOpen}
                onClose={() => setIsModalOpen(false)}
                onSubmit={handleCreateRoute}
                initialData={editingRoute}
            />

            {/* Smart Delete Dialog */}
            {routeToDelete && (
                <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
                    <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-md p-6">
                        <h3 className="text-xl font-bold text-white mb-2">Delete Patched Route?</h3>
                        <p className="text-gray-400 mb-6">
                            This route was created by patching an Ingress. Do you want to revert the Ingress changes or just remove the configuration from Smart Proxy?
                        </p>
                        <div className="flex flex-col gap-3">
                            <Button variant="primary" onClick={() => handleUnpatch(routeToDelete)} className="w-full justify-center bg-blue-600 hover:bg-blue-500">
                                Unpatch Ingress & Delete
                            </Button>
                            <Button variant="danger" onClick={() => deleteRoute(routeToDelete)} className="w-full justify-center">
                                Delete Configuration Only
                            </Button>
                            <Button variant="secondary" onClick={() => setRouteToDelete(null)} className="w-full justify-center mt-2">
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
            className={`flex items-center space-x-2 px-4 py-2 rounded-md text-sm font-medium transition-all ${active
                ? "bg-blue-600 text-white shadow-md"
                : "text-gray-400 hover:text-white hover:bg-gray-700"
                }`}
        >
            {icon}
            <span>{label}</span>
        </button>
    );
}
