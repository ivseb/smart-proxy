import { useState, useEffect } from "react";
import { Button } from "@/components/ui/Button";
import { RefreshCw, Shield, Undo2, Route as RouteIcon, Globe } from "lucide-react";
import { toast } from "sonner";

interface PatchableResource {
    name: string;
    namespace: string;
    host: string;
    service: string;
    port: number;
    patched: boolean;
    status: string;
    type: "Ingress" | "Route";
}

export function PatchingView() {
    const [resources, setResources] = useState<PatchableResource[]>([]);
    const [loading, setLoading] = useState(false);

    const fetchResources = async () => {
        setLoading(true);
        try {
            const [ingRes, routeRes] = await Promise.all([
                fetch("/api/k8s/ingresses?namespace=smart-proxy-demo"),
                fetch("/api/k8s/routes?namespace=smart-proxy-demo")
            ]);

            const ingresses: PatchableResource[] = await ingRes.json();
            const routes: PatchableResource[] = await routeRes.json();

            // Handle potential null/errors if endpoints fail gracefully
            const validIngresses = Array.isArray(ingresses) ? ingresses : [];
            const validRoutes = Array.isArray(routes) ? routes : [];

            setResources([...validIngresses, ...validRoutes]);
        } catch (e) {
            console.error(e);
            toast.error("Failed to fetch resources");
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        fetchResources();
    }, []);

    const patchResource = async (res: PatchableResource) => {
        const endpoint = res.type === "Route" ? "/api/patch-route" : "/api/patch-ingress";
        try {
            await fetch(`${endpoint}?name=${res.name}`, {
                method: "POST"
            });
            toast.success(`Successfully patched ${res.type} ${res.name}`);
            fetchResources();
        } catch (e) {
            toast.error(`Failed to patch ${res.type}`);
        }
    };

    const unpatchResource = async (res: PatchableResource) => {
        const endpoint = res.type === "Route" ? "/api/unpatch-route" : "/api/unpatch-ingress";
        try {
            await fetch(`${endpoint}?name=${res.name}`, {
                method: "POST"
            });
            toast.success(`Restored original backend for ${res.name}`);
            fetchResources();
        } catch (e) {
            toast.error(`Failed to unpatch ${res.type}`);
        }
    };

    return (
        <div className="space-y-4">
            <div className="flex justify-between items-center">
                <h2 className="text-xl font-bold text-white flex items-center gap-2">
                    Routing & Patching
                </h2>
                <Button variant="ghost" size="sm" onClick={fetchResources} disabled={loading}>
                    <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
                </Button>
            </div>

            <div className="grid gap-4 grid-cols-1 md:grid-cols-2 lg:grid-cols-3">
                {resources.length === 0 && !loading && (
                    <div className="col-span-full text-center text-gray-400 py-8">
                        No Ingresses or Routes found.
                    </div>
                )}

                {resources.map(res => (
                    <div key={`${res.type}-${res.name}`} className="bg-gray-800 border border-gray-700 rounded-lg p-4 shadow-lg hover:border-blue-500/50 transition-colors relative overflow-hidden">
                        {/* Type Badge */}
                        <div className="absolute top-0 right-0 bg-gray-700 px-2 py-1 rounded-bl text-xs font-mono text-gray-300 flex items-center gap-1">
                            {res.type === "Route" ? <RouteIcon className="w-3 h-3" /> : <Globe className="w-3 h-3" />}
                            {res.type}
                        </div>

                        <div className="flex justify-between items-start mb-2 pr-12">
                            <h3 className="font-bold text-white truncate w-full" title={res.name}>{res.name}</h3>
                        </div>

                        <div className="mb-2">
                            <span className="bg-blue-900/30 text-blue-300 text-xs px-2 py-0.5 rounded border border-blue-500/20">{res.namespace}</span>
                        </div>

                        <div className="text-sm text-gray-400 mb-4 space-y-1">
                            <div className="flex items-center gap-2">
                                <Globe className="w-3 h-3" />
                                <span className="text-white truncate" title={res.host}>{res.host}</span>
                            </div>
                            <div className="flex items-center gap-2">
                                <span className="text-xs font-mono bg-gray-900 px-1 rounded">SVC</span>
                                <span>{res.service}:{res.port}</span>
                            </div>
                            <div className="flex items-center gap-2">
                                <span className={`w-2 h-2 rounded-full ${res.status.includes("Ready") ? "bg-green-500" : res.status.includes("Sleep") ? "bg-yellow-500" : "bg-red-500"}`} />
                                <span>{res.status}</span>
                            </div>
                        </div>

                        <div className="flex gap-2 mt-4">
                            {res.patched ? (
                                <Button onClick={() => unpatchResource(res)} variant="danger" className="w-full flex items-center justify-center space-x-2">
                                    <Undo2 className="w-4 h-4" />
                                    <span>Unpatch</span>
                                </Button>
                            ) : (
                                <Button onClick={() => patchResource(res)} variant="primary" className="w-full flex items-center justify-center space-x-2">
                                    <Shield className="w-4 h-4" />
                                    <span>Patch Route</span>
                                </Button>
                            )}
                        </div>
                    </div>
                ))}
            </div>
        </div>
    );
}
