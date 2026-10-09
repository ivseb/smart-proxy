import { useState } from "react";
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/Card";
import { Activity, Moon, Server, Zap } from "lucide-react";
import type { ClusterInfo, RouteStatus, StatsData } from "@/types/api";
import { StatusDot } from "@/components/ui/StatusBadge";



interface ChartPoint {
    at: number; // ms since epoch
    time: string;
    requests: number;
}

interface StatsViewProps {
    stats: StatsData | null;
    fetchedAt: number; // When stats was polled
    routes: RouteStatus[];
    info: ClusterInfo | null;
}

export function StatsView({ stats, fetchedAt, routes, info }: StatsViewProps) {
    const [history, setHistory] = useState<ChartPoint[]>([]);
    const [recordedAt, setRecordedAt] = useState(0);

    // Accumulate one chart point per poll (state derived during render, not in an effect).
    if (stats && fetchedAt !== recordedAt) {
        setRecordedAt(fetchedAt);
        const time = new Date(fetchedAt).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
        setHistory(prev => [...prev, { at: fetchedAt, time, requests: stats.TotalRequests }].slice(-20));
    }

    // Requests per second between the last two polls
    const last = history[history.length - 1];
    const prev = history[history.length - 2];
    const rps = last && prev && last.at > prev.at
        ? (last.requests - prev.requests) / ((last.at - prev.at) / 1000)
        : 0;

    // Requests per second for each polling interval, for the chart.
    const rates = history.slice(1).map((p, i) => ({
        time: p.time,
        rps: p.at > history[i].at ? Math.max(0, (p.requests - history[i].requests) / ((p.at - history[i].at) / 1000)) : 0,
    }));

    const managed = routes.filter(r => r.source && !r.always_on);
    const asleep = managed.filter(r => r.status === "Sleep").length;
    const issues = routes.filter(r => r.status === "Error" || r.status === "Unwatched").length;

    const byNamespace = new Map<string, RouteStatus[]>();
    for (const ns of info?.namespaces || []) byNamespace.set(ns, []);
    for (const r of routes) byNamespace.set(r.namespace, [...(byNamespace.get(r.namespace) || []), r]);
    const namespaces = [...byNamespace.entries()].sort(([a], [b]) => a.localeCompare(b));

    return (
        <div className="space-y-6">
            <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
                <Metric label="Asleep now" icon={<Moon className="w-5 h-5 text-slate-300" />} tint="bg-slate-500/10"
                    value={`${asleep} / ${managed.length}`} hint="deployments with an idle timer" />
                <Metric label="Requests / sec" icon={<Zap className="w-5 h-5 text-green-400" />} tint="bg-green-500/10"
                    value={rps.toFixed(1)} hint="over the last poll" />
                <Metric label="Total requests" icon={<Activity className="w-5 h-5 text-blue-400" />} tint="bg-blue-500/10"
                    value={String(stats?.TotalRequests || 0)} hint="since Smart Proxy started" />
                <Metric label="Cluster" icon={<Server className="w-5 h-5 text-purple-400" />} tint="bg-purple-500/10"
                    value={!info ? "…" : !info.connected ? "Offline" : issues > 0 ? `${issues} issue${issues === 1 ? "" : "s"}` : "Healthy"}
                    valueClass={!info?.connected || issues > 0 ? "text-orange-300" : "text-green-400"}
                    hint={info?.connected ? info.scope : "no Kubernetes connection"} />
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-[2fr_1fr] gap-6">
                <Card>
                    <CardHeader>
                        <CardTitle>Traffic</CardTitle>
                        <p className="text-gray-400 text-sm">Requests per second through Smart Proxy</p>
                    </CardHeader>
                    <CardContent>
                        <div className="h-[260px] w-full">
                            <ResponsiveContainer width="100%" height="100%">
                                <AreaChart data={rates}>
                                    <defs>
                                        <linearGradient id="colorReq" x1="0" y1="0" x2="0" y2="1">
                                            <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3} />
                                            <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                                        </linearGradient>
                                    </defs>
                                    <CartesianGrid strokeDasharray="3 3" stroke="#374151" vertical={false} />
                                    <XAxis dataKey="time" stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} />
                                    <YAxis stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} allowDecimals={false} width={32} />
                                    <Tooltip
                                        contentStyle={{ backgroundColor: "#1f2937", borderColor: "#374151", color: "#fff" }}
                                        itemStyle={{ color: "#60a5fa" }}
                                        formatter={(v) => [`${Number(v).toFixed(1)} req/s`, "Traffic"]}
                                    />
                                    <Area type="monotone" dataKey="rps" stroke="#3b82f6" strokeWidth={2} fillOpacity={1} fill="url(#colorReq)" />
                                </AreaChart>
                            </ResponsiveContainer>
                        </div>
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>Namespaces</CardTitle>
                        <p className="text-gray-400 text-sm">Routes awake and asleep</p>
                    </CardHeader>
                    <CardContent className="p-0">
                        {namespaces.length === 0 ? (
                            <p className="p-6 text-sm text-gray-500">No namespaces managed.</p>
                        ) : (
                            <ul className="divide-y divide-gray-700/70 max-h-[260px] overflow-y-auto">
                                {namespaces.map(([ns, items]) => {
                                    const awake = items.filter(r => r.status === "Ready" || r.status === "Scaling").length;
                                    const sleeping = items.filter(r => r.status === "Sleep").length;
                                    return (
                                        <li key={ns} className="flex items-center justify-between gap-3 px-6 py-2.5 text-sm">
                                            <span className="font-mono text-gray-200 truncate">{ns}</span>
                                            <span className="flex items-center gap-3 text-xs text-gray-400 shrink-0 tabular-nums">
                                                <span className="flex items-center gap-1"><StatusDot status="Ready" />{awake}</span>
                                                <span className="flex items-center gap-1"><StatusDot status="Sleep" />{sleeping}</span>
                                                <span className="text-gray-500">{items.length} route{items.length === 1 ? "" : "s"}</span>
                                            </span>
                                        </li>
                                    );
                                })}
                            </ul>
                        )}
                    </CardContent>
                </Card>
            </div>
        </div>
    );
}

function Metric({ label, value, hint, icon, tint, valueClass = "text-white" }: {
    label: string; value: string; hint: string; icon: React.ReactNode; tint: string; valueClass?: string;
}) {
    return (
        <Card>
            <CardContent className="pt-5 pb-5">
                <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                        <p className="text-sm font-medium text-gray-400">{label}</p>
                        <div className={`text-2xl font-bold mt-1.5 truncate ${valueClass}`}>{value}</div>
                        <p className="text-xs text-gray-500 mt-1 truncate" title={hint}>{hint}</p>
                    </div>
                    <div className={`p-2.5 rounded-xl shrink-0 ${tint}`}>{icon}</div>
                </div>
            </CardContent>
        </Card>
    );
}
