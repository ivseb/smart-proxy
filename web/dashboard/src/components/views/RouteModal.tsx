import { useState, useEffect } from "react";
import type { RouteConfig } from "@/types/api";
import { Button } from "@/components/ui/Button";
import { X, Plus, ArrowUp, ArrowDown } from "lucide-react";

// For now, custom modal implementation to avoid extra deps if possible, 
// but Headless UI is great. I'll implement a simple customized overlay.

interface RouteModalProps {
    isOpen: boolean;
    onClose: () => void;
    onSubmit: (data: Partial<RouteConfig>) => Promise<void>;
    initialData?: RouteConfig | null;
}

export function RouteModal({ isOpen, onClose, onSubmit, initialData }: RouteModalProps) {
    const [formData, setFormData] = useState<Partial<RouteConfig>>({
        host: "",
        path: "/",
        namespace: "", // Will be auto-discovered
        deployment: "",
        target_service: "",
        target_port: 80,
        dependencies: [],
        inject_badge: false,
        idle_timeout: 30 * 60 * 1000 * 1000 * 1000 // 30m default (ns)
    });

    const [deployments, setDeployments] = useState<string[]>([]);
    const [selectedDepToAdd, setSelectedDepToAdd] = useState("");

    // Reset form when opening
    useEffect(() => {
        if (isOpen) {
            if (initialData) {
                setFormData(initialData);
                // Load deployments for the namespace of the existing route
                fetchDeployments(initialData.namespace);
            } else {
                setFormData({
                    host: "",
                    path: "/",
                    namespace: "",
                    deployment: "",
                    target_service: "",
                    target_port: 80,
                    dependencies: [],
                    inject_badge: false,
                    idle_timeout: 30 * 60 * 1000 * 1000 * 1000
                });
                // Discover namespace and load deployments
                discoverNamespace();
            }
        }
    }, [isOpen, initialData]);

    const discoverNamespace = async () => {
        try {
            const res = await fetch("/api/k8s/namespaces");
            const nss = await res.json();
            // Heuristic: Filter out kube-* and take first likely user NS
            const targetNs = nss.find((n: string) => !n.startsWith('kube-') && n !== 'ingress-nginx') || 'default';
            setFormData(prev => ({ ...prev, namespace: targetNs }));
            fetchDeployments(targetNs);
        } catch (e) {
            console.error("Failed to discover namespace", e);
        }
    };

    const fetchDeployments = async (ns: string | undefined) => {
        if (!ns) return;
        try {
            const res = await fetch(`/api/k8s/deployments?namespace=${ns}`);
            const deps = await res.json();
            setDeployments(deps.filter((d: string) => d !== 'smart-proxy'));
        } catch (e) {
            console.error("Failed to load deployments", e);
        }
    };

    if (!isOpen) return null;

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        await onSubmit(formData);
        onClose();
    };

    const addDependency = () => {
        if (selectedDepToAdd && !formData.dependencies?.some(d => d.name === selectedDepToAdd)) {
            setFormData(prev => ({
                ...prev,
                dependencies: [...(prev.dependencies || []), { name: selectedDepToAdd, stop_on_idle: true }]
            }));
            setSelectedDepToAdd("");
        }
    };

    const removeDependency = (depName: string) => {
        setFormData(prev => ({
            ...prev,
            dependencies: prev.dependencies?.filter(d => d.name !== depName)
        }));
    };

    const moveDependency = (index: number, direction: -1 | 1) => {
        const deps = [...(formData.dependencies || [])];
        const newIndex = index + direction;
        if (newIndex >= 0 && newIndex < deps.length) {
            [deps[index], deps[newIndex]] = [deps[newIndex], deps[index]];
            setFormData(prev => ({ ...prev, dependencies: deps }));
        }
    };

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm">
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-2xl max-h-[90vh] overflow-y-auto">
                <div className="p-6 border-b border-gray-700 flex justify-between items-center">
                    <h2 className="text-xl font-bold text-white">Route Configuration</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white"><X className="w-6 h-6" /></button>
                </div>

                <form onSubmit={handleSubmit} className="p-6 space-y-6">
                    <div className="grid grid-cols-2 gap-4">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1">Host</label>
                            <input
                                type="text"
                                placeholder="app.local"
                                className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                                value={formData.host}
                                onChange={e => setFormData({ ...formData, host: e.target.value })}
                            />
                        </div>
                        <div>
                            <label className="block text-gray-400 text-sm mb-1">Path</label>
                            <input
                                type="text"
                                value={formData.path}
                                className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                                onChange={e => setFormData({ ...formData, path: e.target.value })}
                            />
                        </div>
                    </div>

                    <div>
                        <label className="block text-gray-400 text-sm mb-1">Target Deployment</label>
                        <select
                            className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                            value={formData.deployment}
                            onChange={e => {
                                const dep = e.target.value;
                                setFormData({
                                    ...formData,
                                    deployment: dep,
                                    target_service: dep // Auto-fill service
                                });
                            }}
                        >
                            <option value="">Select Deployment...</option>
                            {deployments.map(d => <option key={d} value={d}>{d}</option>)}
                        </select>
                    </div>

                    <div className="grid grid-cols-2 gap-4">
                        <div>
                            <label className="block text-gray-400 text-sm mb-1">Service Name</label>
                            <input
                                type="text"
                                value={formData.target_service}
                                className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                                onChange={e => setFormData({ ...formData, target_service: e.target.value })}
                            />
                        </div>
                        <div>
                            <label className="block text-gray-400 text-sm mb-1">Port</label>
                            <input
                                type="number"
                                value={formData.target_port}
                                className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                                onChange={e => setFormData({ ...formData, target_port: parseInt(e.target.value) })}
                            />
                        </div>
                    </div>

                    {/* Dependencies */}
                    <div className="bg-gray-700/30 p-4 rounded-lg border border-gray-700">
                        <label className="block text-gray-300 text-sm font-medium mb-3">Dependencies (Start Order)</label>

                        <div className="space-y-2 mb-4">
                            {formData.dependencies?.map((dep, idx) => (
                                <div key={dep.name} className="flex justify-between items-center bg-gray-800 p-2 rounded border border-gray-600">
                                    <div className="flex items-center space-x-3">
                                        <span className="text-gray-500 font-mono text-xs">{idx + 1}.</span>
                                        <span className="text-white font-medium">{dep.name}</span>
                                        <label className="flex items-center space-x-1.5 cursor-pointer bg-gray-700 px-2 py-0.5 rounded border border-gray-600 hover:bg-gray-600 transition-colors">
                                            <input
                                                type="checkbox"
                                                className="w-3.5 h-3.5 rounded text-red-500 focus:ring-red-500 bg-gray-800 border-gray-500"
                                                checked={dep.stop_on_idle}
                                                onChange={() => {
                                                    const newDeps = [...(formData.dependencies || [])];
                                                    newDeps[idx] = { ...dep, stop_on_idle: !dep.stop_on_idle };
                                                    setFormData({ ...formData, dependencies: newDeps });
                                                }}
                                            />
                                            <span className="text-xs text-gray-300">Stop on Idle</span>
                                        </label>
                                    </div>
                                    <div className="flex items-center space-x-1">
                                        <button type="button" onClick={() => moveDependency(idx, -1)} className="text-gray-400 hover:text-white p-1" disabled={idx === 0}><ArrowUp size={14} /></button>
                                        <button type="button" onClick={() => moveDependency(idx, 1)} className="text-gray-400 hover:text-white p-1" disabled={idx === (formData.dependencies?.length || 0) - 1}><ArrowDown size={14} /></button>
                                        <button type="button" onClick={() => removeDependency(dep.name)} className="text-red-400 hover:text-red-300 p-1 ml-2"><X size={14} /></button>
                                    </div>
                                </div>
                            ))}
                            {(!formData.dependencies || formData.dependencies.length === 0) && (
                                <div className="text-gray-500 text-sm italic">No dependencies configured.</div>
                            )}
                        </div>

                        <div className="flex space-x-2">
                            <select
                                className="flex-1 bg-gray-600/50 border border-gray-600 rounded-lg px-3 py-1.5 text-sm text-white focus:outline-none"
                                value={selectedDepToAdd}
                                onChange={e => setSelectedDepToAdd(e.target.value)}
                            >
                                <option value="">Add Dependency...</option>
                                {deployments
                                    .filter(d => d !== formData.deployment && !formData.dependencies?.some(dep => dep.name === d))
                                    .map(d => <option key={d} value={d}>{d}</option>)
                                }
                            </select>
                            <Button type="button" size="sm" onClick={addDependency} disabled={!selectedDepToAdd}><Plus size={16} /></Button>
                        </div>
                    </div>

                    <div>
                        <label className="block text-gray-400 text-sm mb-1">Idle Timeout (e.g. 30m, 1h)</label>
                        <input
                            type="text"
                            placeholder="30m"
                            className="w-full bg-gray-700 border border-gray-600 rounded-lg px-4 py-2 focus:ring-2 focus:ring-blue-500 outline-none text-white"
                            // Convert ns to string for display? Or just store string in form?
                            // Logic: formData.idle_timeout is number (ns). We need to convert to string.
                            // But wait, the previous code had string "30m" in input.
                            // Let's assume input is text, and on submit we parse it or backend handles it?
                            // Actually, RouteConfig expects number. So we need conversion logic.
                            // Simplification: Store raw string in local state, parse on submit.
                            // But formData is RouteConfig (typed). 
                            // Quick fix: Add a local string state or just cast it here.
                            // Let's use a helper function for display.
                            defaultValue={formData.idle_timeout ? (formData.idle_timeout / 60000000000) + "m" : "30m"}
                            onChange={e => {
                                const val = e.target.value;
                                // Simple parse: if ends with 'm', * 60*10^9. If 's', * 10^9.
                                // This is tricky in inline change. 
                                // Better: Update formData with a rough calc or keep it simple.
                                let ns = 30 * 60 * 1000 * 1000 * 1000;
                                if (val.endsWith('m')) ns = parseInt(val) * 60 * 1000 * 1000 * 1000;
                                else if (val.endsWith('s')) ns = parseInt(val) * 1000 * 1000 * 1000;
                                else ns = parseInt(val) * 60 * 1000 * 1000 * 1000; // Default minutes

                                setFormData({ ...formData, idle_timeout: ns });
                            }}
                        />
                        <p className="text-xs text-gray-500 mt-1">Format: 30m, 1h, 60s</p>
                    </div>

                    <div className="flex items-center space-x-2">
                        <input
                            type="checkbox"
                            id="inject_badge"
                            className="w-4 h-4 rounded text-blue-600 focus:ring-blue-500 bg-gray-700 border-gray-600"
                            checked={formData.inject_badge}
                            onChange={e => setFormData({ ...formData, inject_badge: e.target.checked })}
                        />
                        <label htmlFor="inject_badge" className="text-white text-sm">Inject Proxy Badge</label>
                    </div>

                    <div className="flex justify-end space-x-3 pt-4 border-t border-gray-700">
                        <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
                        <Button type="submit">Save Route</Button>
                    </div>
                </form>
            </div>
        </div>
    );
}
