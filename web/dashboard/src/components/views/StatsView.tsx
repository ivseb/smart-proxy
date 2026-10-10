import { useState } from "react";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/Card";
import { Activity, Moon, Server, Zap } from "lucide-react";
import type { ClusterInfo, RouteStatus, StatsData } from "@/types/api";
import { StatusDot } from "@/components/ui/StatusBadge";
import { RequestsChart } from "@/components/views/RequestsChart";
import { t, tn } from "@/lib/i18n";



interface Sample {
    at: number; // ms since epoch
    requests: number;
}

interface StatsViewProps {
    stats: StatsData | null;
    fetchedAt: number; // When stats was polled
    routes: RouteStatus[];
    info: ClusterInfo | null;
}

export function StatsView({ stats, fetchedAt, routes, info }: StatsViewProps) {
    const [history, setHistory] = useState<Sample[]>([]);
    const [recordedAt, setRecordedAt] = useState(0);

    // Keep the last two polls for the live rate (state derived during render, not in an effect).
    if (stats && fetchedAt !== recordedAt) {
        setRecordedAt(fetchedAt);
        setHistory(prev => [...prev, { at: fetchedAt, requests: stats.TotalRequests }].slice(-2));
    }

    // Requests per second between the last two polls
    const last = history[history.length - 1];
    const prev = history[history.length - 2];
    const rps = last && prev && last.at > prev.at
        ? Math.max(0, (last.requests - prev.requests) / ((last.at - prev.at) / 1000))
        : 0;

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
                <Metric label={t("Asleep now")} icon={<Moon className="w-5 h-5 text-slate-300" />} tint="bg-slate-500/10"
                    value={`${asleep} / ${managed.length}`} hint={t("deployments with an idle timer")} />
                <Metric label={t("Requests / sec")} icon={<Zap className="w-5 h-5 text-green-400" />} tint="bg-green-500/10"
                    value={rps.toFixed(1)} hint={t("over the last poll")} />
                <Metric label={t("Total requests")} icon={<Activity className="w-5 h-5 text-blue-400" />} tint="bg-blue-500/10"
                    value={String(stats?.TotalRequests || 0)} hint={t("since Smart Proxy started")} />
                <Metric label={t("Cluster")} icon={<Server className="w-5 h-5 text-purple-400" />} tint="bg-purple-500/10"
                    value={!info ? "…" : !info.connected ? t("Offline") : issues > 0 ? tn(issues, "{n} issue", "{n} issues") : t("Healthy")}
                    valueClass={!info?.connected || issues > 0 ? "text-orange-300" : "text-green-400"}
                    hint={info?.connected ? info.scope : t("no Kubernetes connection")} />
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-[2fr_1fr] gap-6">
                <Card>
                    <CardHeader>
                        <CardTitle>{t("Traffic")}</CardTitle>
                        <p className="text-gray-400 text-sm">{t("Requests per second through Smart Proxy")}</p>
                    </CardHeader>
                    <CardContent>
                        <RequestsChart />
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>{t("Namespaces")}</CardTitle>
                        <p className="text-gray-400 text-sm">{t("Routes awake and asleep")}</p>
                    </CardHeader>
                    <CardContent className="p-0">
                        {namespaces.length === 0 ? (
                            <p className="p-6 text-sm text-gray-500">{t("No namespaces managed.")}</p>
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
                                                <span className="text-gray-500">{tn(items.length, "{n} route", "{n} routes")}</span>
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
