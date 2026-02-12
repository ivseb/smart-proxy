import { useState, useEffect } from "react";
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/Card";
import { Activity, Zap, Server } from "lucide-react";
import type { StatsData } from "@/types/api";



interface ChartPoint {
    time: string;
    requests: number;
}

interface StatsViewProps {
    stats: StatsData | null;
}

export function StatsView({ stats }: StatsViewProps) {
    // const { data: stats } = usePolling<StatsData>("/api/stats", 2000); // Lifted to Dashboard
    const [history, setHistory] = useState<ChartPoint[]>([]);

    // Accumulate history for the chart
    useEffect(() => {
        if (stats) {
            setHistory(prev => {
                const now = new Date();
                const timeStr = now.toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
                const newPoint = { time: timeStr, requests: stats.TotalRequests };

                // Keep last 20 points
                const newHistory = [...prev, newPoint];
                if (newHistory.length > 20) newHistory.shift();
                return newHistory;
            });
        }
    }, [stats]);

    // Calculate RPS (approximate based on polling interval)
    const rps = history.length > 1
        ? (history[history.length - 1].requests - history[history.length - 2].requests) / 2
        : 0;

    return (
        <div className="space-y-6">
            {/* Metric Cards */}
            <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
                <Card>
                    <CardContent className="pt-6">
                        <div className="flex items-center justify-between">
                            <div>
                                <p className="text-sm font-medium text-gray-400">Total Requests</p>
                                <div className="text-3xl font-bold text-white mt-2">{stats?.TotalRequests || 0}</div>
                            </div>
                            <div className="p-3 bg-blue-500/10 rounded-xl">
                                <Activity className="w-6 h-6 text-blue-400" />
                            </div>
                        </div>
                    </CardContent>
                </Card>

                <Card>
                    <CardContent className="pt-6">
                        <div className="flex items-center justify-between">
                            <div>
                                <p className="text-sm font-medium text-gray-400">Requests / Sec</p>
                                <div className="text-3xl font-bold text-white mt-2">{rps.toFixed(1)}</div>
                            </div>
                            <div className="p-3 bg-green-500/10 rounded-xl">
                                <Zap className="w-6 h-6 text-green-400" />
                            </div>
                        </div>
                    </CardContent>
                </Card>

                <Card>
                    <CardContent className="pt-6">
                        <div className="flex items-center justify-between">
                            <div>
                                <p className="text-sm font-medium text-gray-400">System Status</p>
                                <div className="text-3xl font-bold text-green-400 mt-2">Operational</div>
                            </div>
                            <div className="p-3 bg-purple-500/10 rounded-xl">
                                <Server className="w-6 h-6 text-purple-400" />
                            </div>
                        </div>
                    </CardContent>
                </Card>
            </div>

            {/* Main Chart */}
            <Card className="col-span-1">
                <CardHeader>
                    <CardTitle>Traffic Overview</CardTitle>
                    <p className="text-gray-400 text-sm">Real-time request processing (Growth)</p>
                </CardHeader>
                <CardContent>
                    <div className="h-[300px] w-full">
                        <ResponsiveContainer width="100%" height="100%">
                            <AreaChart data={history}>
                                <defs>
                                    <linearGradient id="colorReq" x1="0" y1="0" x2="0" y2="1">
                                        <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3} />
                                        <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                                    </linearGradient>
                                </defs>
                                <CartesianGrid strokeDasharray="3 3" stroke="#374151" vertical={false} />
                                <XAxis dataKey="time" stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} />
                                <YAxis stroke="#9ca3af" fontSize={12} tickLine={false} axisLine={false} />
                                <Tooltip
                                    contentStyle={{ backgroundColor: '#1f2937', borderColor: '#374151', color: '#fff' }}
                                    itemStyle={{ color: '#60a5fa' }}
                                />
                                <Area type="monotone" dataKey="requests" stroke="#3b82f6" strokeWidth={2} fillOpacity={1} fill="url(#colorReq)" />
                            </AreaChart>
                        </ResponsiveContainer>
                    </div>
                </CardContent>
            </Card>
        </div>
    );
}
