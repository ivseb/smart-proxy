import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { PeriodPicker } from "@/components/views/UptimeParts";
import { UptimeHistory } from "@/components/views/UptimeHistory";
import { useStoredState } from "@/hooks/useStoredState";
import { usePolling } from "@/hooks/usePolling";
import { t } from "@/lib/i18n";
import { canonical, periodTitle, uptimeURL, type Period } from "@/lib/uptime";
import type { RouteStatus, UptimeReport, WorkloadUptime } from "@/types/api";

// UptimeCard shows how long the route's workloads were up over a period, and when exactly.
export function UptimeCard({ route }: { route: RouteStatus }) {
    const [period, setPeriod] = useStoredState<Period>("uptime.route-period", { kind: "30d" });
    const { data, error } = usePolling<UptimeReport>(uptimeURL(period, { route: route.id, hours: "1", changes: "1" }), 120_000);
    if (error && !data) return null; // No history (not connected to a cluster)

    const main = canonical(route.deployment);
    const backends = new Set((route.backends || []).map(b => b.workload).filter(Boolean).map(w => canonical(w as string)));
    const rank = (w: WorkloadUptime) => (w.workload === main ? 0 : backends.has(w.workload) ? 1 : 2);
    const roles = (w: WorkloadUptime) => [null, t("backend"), t("dependency")][rank(w)];
    const report = data && { ...data, workloads: [...data.workloads].sort((a, b) => rank(a) - rank(b)) };

    return (
        <Card>
            <CardHeader className="flex flex-wrap items-start justify-between gap-3">
                <div>
                    <CardTitle>{periodTitle(period, data)}</CardTitle>
                    <p className="text-gray-400 text-sm">{t("Time with at least one replica ({zone})", { zone: data?.timezone || "" })}</p>
                </div>
                <PeriodPicker value={period} onChange={setPeriod} />
            </CardHeader>
            <CardContent>
                {!report ? (
                    <div className="text-sm text-gray-500">{t("Loading…")}</div>
                ) : report.workloads.length === 0 ? (
                    <div className="text-sm text-gray-500 italic">{t("No history yet: Smart Proxy records it every minute from now on.")}</div>
                ) : (
                    <UptimeHistory key={JSON.stringify(period)} report={report} period={period} roles={roles} />
                )}
            </CardContent>
        </Card>
    );
}
