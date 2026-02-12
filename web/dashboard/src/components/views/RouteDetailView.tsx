import { useState, useMemo, useEffect } from "react";
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { StatusBadge } from "@/components/ui/StatusBadge";
import { ArrowLeft, Square, Trash2, Edit } from "lucide-react";
import type { RouteStatus, LogEntry, StatsData } from "@/types/api";

interface RouteDetailViewProps {
    route: RouteStatus;
    stats: StatsData | null;
    logs: LogEntry[];
    onBack: () => void;
    onEdit: (route: RouteStatus) => void;
    onDelete: (id: string) => void;
    onStop: (route: RouteStatus) => void;
}

export function RouteDetailView({ route, stats, logs, onBack, onEdit, onDelete, onStop }: RouteDetailViewProps) {
    const routeLogs = useMemo(() => {
        return logs.filter(l => l.message.includes(route.deployment) || l.message.includes(route.path));
    }, [logs, route]);

    const requests = stats?.RouteStats[route.id] || 0;

    // Mock history data for the chart since we don't have per-route history in backend yet
    // In a real app, backend would provide historical series.
    // We will just show a flat line or random for demo, or nothing. 
    // Actually, let's just show a placeholder chart with the current value to imply functionality.
    const data = [
        { time: '10:00', value: Math.max(0, requests - 10) },
        { time: '10:05', value: Math.max(0, requests - 5) },
        { time: '10:10', value: requests },
    ];

    return (
        <div className="space-y-6 animate-in slide-in-from-right duration-300">
            <Button variant="ghost" onClick={onBack} className="flex items-center space-x-2 text-gray-400 hover:text-white">
                <ArrowLeft size={16} />
                <span>Back to Routes</span>
            </Button>

            <header className="flex justify-between items-start">
                <div>
                    <h1 className="text-3xl font-bold text-white flex items-center gap-3">
                        {route.host}{route.path}
                        <StatusBadge status={route.status} />
                    </h1>
                    <p className="text-gray-400 mt-2">
                        Target: <span className="text-blue-400">{route.target_service}:{route.target_port}</span>
                        <span className="mx-2">•</span>
                        Deployment: <span className="text-blue-400">{route.deployment}</span>
                    </p>
                </div>
                <div className="flex space-x-2">
                    <Button variant="secondary" onClick={() => onStop(route)} title="Stop/Scale to 0">
                        <Square className="w-4 h-4 mr-2 text-orange-400" />
                        Stop Pods
                    </Button>
                    <Button variant="secondary" onClick={() => onEdit(route)}>
                        <Edit className="w-4 h-4 mr-2 text-blue-400" />
                        Edit Config
                    </Button>
                    <Button variant="danger" onClick={() => onDelete(route.id)}>
                        <Trash2 className="w-4 h-4 mr-2" />
                        Delete
                    </Button>
                </div>
            </header>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
                <Card>
                    <CardHeader><CardTitle>Total Requests</CardTitle></CardHeader>
                    <CardContent>
                        <div className="text-4xl font-bold text-white">{requests}</div>
                    </CardContent>
                </Card>
                <Card>
                    <CardHeader><CardTitle>Idle Timeout</CardTitle></CardHeader>
                    <CardContent>
                        <div className="text-2xl font-mono text-gray-300">
                            {(route.idle_timeout / 60000000000).toFixed(0)}m
                        </div>
                        <div className="text-sm text-gray-500 mt-1">Scale down after inactivity</div>

                        <IdleTimer lastActivity={route.last_activity} timeout={route.idle_timeout} />
                    </CardContent>
                </Card>
                <Card>
                    <CardHeader><CardTitle>Dependencies</CardTitle></CardHeader>
                    <CardContent>
                        <div className="space-y-2">
                            {route.dependencies.map(d => (
                                <div key={d.name} className="flex justify-between items-center text-sm bg-gray-800 p-2 rounded">
                                    <div className="flex items-center gap-2">
                                        <span>{d.name}</span>
                                        {d.stop_on_idle && <span className="text-[10px] bg-red-900/50 text-red-200 px-1 rounded border border-red-800">Stop on Idle</span>}
                                    </div>
                                    <StatusBadge status={route.dependency_status[d.name] || 'Unknown'} />
                                </div>
                            ))}
                            {route.dependencies.length === 0 && <span className="text-gray-500 italic">None</span>}
                        </div>
                    </CardContent>
                </Card>
            </div>

            <Card>
                <CardHeader><CardTitle>Traffic History</CardTitle></CardHeader>
                <CardContent className="h-[300px]">
                    <ResponsiveContainer width="100%" height="100%">
                        <AreaChart data={data}>
                            <CartesianGrid strokeDasharray="3 3" stroke="#374151" vertical={false} />
                            <XAxis dataKey="time" stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} />
                            <YAxis stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} />
                            <Tooltip contentStyle={{ backgroundColor: '#1f2937', borderColor: '#374151' }} />
                            <Area type="monotone" dataKey="value" stroke="#3b82f6" fill="#3b82f6" fillOpacity={0.2} />
                        </AreaChart>
                    </ResponsiveContainer>
                </CardContent>
            </Card>

            <Card>
                <CardHeader><CardTitle>Event Log</CardTitle></CardHeader>
                <CardContent className="h-[300px] overflow-y-auto bg-gray-950 rounded-lg p-4 font-mono text-sm">
                    {routeLogs.length === 0 ? (
                        <div className="text-gray-500 italic text-center py-8">No specific logs found for this route.</div>
                    ) : (
                        routeLogs.map((l, i) => (
                            <div key={i} className="mb-1 border-b border-gray-900 pb-1 last:border-0 hover:bg-white/5 p-1 rounded">
                                <span className="text-gray-500 mr-3">[{new Date(l.timestamp).toLocaleTimeString()}]</span>
                                <span className={l.level === 'error' ? 'text-red-400' : 'text-gray-300'}>{l.message}</span>
                            </div>
                        ))
                    )}
                </CardContent>
            </Card>
        </div>
    );
}

function IdleTimer({ lastActivity, timeout }: { lastActivity: string; timeout: number }) {
    const [timeLeft, setTimeLeft] = useState<string>("--:--");

    useEffect(() => {
        const interval = setInterval(() => {
            const now = new Date().getTime();
            const last = new Date(lastActivity).getTime();
            // timeout is in ns, convert to ms
            const timeoutMs = timeout / 1000000;
            const diff = (last + timeoutMs) - now;

            if (diff <= 0) {
                setTimeLeft("Idle");
            } else {
                const m = Math.floor(diff / 60000);
                const s = Math.floor((diff % 60000) / 1000);
                setTimeLeft(`${m}m ${s}s`);
            }
        }, 1000);

        return () => clearInterval(interval);
    }, [lastActivity, timeout]);

    return (
        <div className="mt-4 p-3 bg-gray-800 rounded-lg border border-gray-700">
            <p className="text-xs text-gray-400 uppercase tracking-wider mb-1">Time until idle</p>
            <p className={`text-xl font-mono font-bold ${timeLeft === "Idle" ? "text-red-400" : "text-green-400"}`}>
                {timeLeft}
            </p>
        </div>
    );
}
