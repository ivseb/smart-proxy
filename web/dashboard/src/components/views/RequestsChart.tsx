import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts";
import { usePolling } from "@/hooks/usePolling";
import type { StatsHistory } from "@/types/api";

interface RequestsChartProps {
    routeId?: string; // All routes when omitted
    height?: number;
}

// Requests per second over the recent past, as kept in memory by Smart Proxy (STATS_RETENTION).
export function RequestsChart({ routeId, height = 260 }: RequestsChartProps) {
    const { data, fetchedAt } = usePolling<StatsHistory>("/api/stats/history", 10000);
    const interval = data?.interval || 10;
    const points = (data?.points || []).map(p => ({
        at: new Date(p.at).getTime(),
        rps: (routeId ? p.routes?.[routeId] || 0 : p.requests) / interval,
    }));
    // The axis always spans the whole retention, ending at the latest point: a few minutes of data
    // show as a few minutes, not stretched across the chart.
    const end = points.length ? points[points.length - 1].at : fetchedAt;
    const start = end - (data?.retention || 1800) * 1000;
    const step = (data?.retention || 1800) > 3600 ? 15 * 60_000 : 5 * 60_000;
    const ticks: number[] = [];
    for (let t = Math.ceil(start / step) * step; t <= end; t += step) ticks.push(t);
    const clock = (t: number, seconds = false) =>
        new Date(t).toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", ...(seconds ? { second: "2-digit" } : {}) });
    const gradient = `requests-${routeId || "all"}`.replace(/[^\w-]/g, "_");

    if (data && points.length === 0) {
        return <div style={{ height }} className="flex items-center justify-center text-sm text-gray-500">Collecting data…</div>;
    }
    const minutes = Math.round((data?.retention || 0) / 60);
    return (
        <div style={{ height }} className="w-full relative">
            {minutes > 0 && <span className="absolute right-0 -top-6 text-xs text-gray-500">last {minutes} min</span>}
            <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={points}>
                    <defs>
                        <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
                            <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3} />
                            <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                        </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray="3 3" stroke="#374151" vertical={false} />
                    <XAxis dataKey="at" type="number" scale="time" domain={[start, end]} ticks={ticks} tickFormatter={t => clock(t)}
                        stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} minTickGap={24} />
                    <YAxis stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} width={40} />
                    <Tooltip
                        contentStyle={{ backgroundColor: "#1f2937", borderColor: "#374151", color: "#fff" }}
                        itemStyle={{ color: "#60a5fa" }}
                        formatter={(v) => [`${Number(v).toFixed(2)} req/s`, "Traffic"]}
                        labelFormatter={t => clock(Number(t), true)}
                    />
                    <Area type="monotoneX" dataKey="rps" stroke="#3b82f6" strokeWidth={2} fillOpacity={1} fill={`url(#${gradient})`} isAnimationActive={false} />
                </AreaChart>
            </ResponsiveContainer>
        </div>
    );
}
