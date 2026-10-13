import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { UptimeBars } from "@/components/views/UptimeBars";
import { useStoredState } from "@/hooks/useStoredState";
import { usePolling } from "@/hooks/usePolling";
import { workloadLabel } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { canonical, downMinutes, hours, percent, sinceNote, upShare, uptimeURL } from "@/lib/uptime";
import type { RouteStatus, UptimeReport, WorkloadUptime } from "@/types/api";

// UptimeCard shows how long the route's workloads were up over the last 7 or 30 days.
export function UptimeCard({ route }: { route: RouteStatus }) {
    const [days, setDays] = useStoredState<number>("uptime.days", 30);
    const { data, error } = usePolling<UptimeReport>(uptimeURL(days, route.id), 300_000);
    if (error && !data) return null; // No history (not connected to a cluster)

    const main = canonical(route.deployment);
    const backends = new Set((route.backends || []).map(b => b.workload).filter(Boolean).map(w => canonical(w as string)));
    const role = (w: WorkloadUptime) => (w.workload === main ? 0 : backends.has(w.workload) ? 1 : 2);
    const workloads = [...(data?.workloads || [])].sort((a, b) => role(a) - role(b));
    const note = data && sinceNote(data);

    return (
        <Card>
            <CardHeader className="flex flex-wrap items-start justify-between gap-3">
                <div>
                    <CardTitle>{tn(days, "Last day", "Last {n} days")}</CardTitle>
                    <p className="text-gray-400 text-sm">{t("Time with at least one replica, day by day ({zone})", { zone: data?.timezone || "" })}</p>
                </div>
                <PeriodToggle days={days} onChange={setDays} />
            </CardHeader>
            <CardContent className="space-y-5">
                {!data ? (
                    <div className="text-sm text-gray-500">{t("Loading…")}</div>
                ) : workloads.length === 0 ? (
                    <div className="text-sm text-gray-500 italic">{t("No history yet: Smart Proxy records it every minute from now on.")}</div>
                ) : (
                    workloads.map(w => <WorkloadHistory key={w.workload} w={w} role={role(w)} />)
                )}
                {note && <p className="text-xs text-gray-500">{note}</p>}
            </CardContent>
        </Card>
    );
}

// role: 0 the route's workload, 1 another backend, 2 a dependency.
function WorkloadHistory({ w, role }: { w: WorkloadUptime; role: number }) {
    return (
        <div className="space-y-2">
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <span className="font-mono text-sm text-gray-100">
                    {workloadLabel(w.workload)}
                    {role > 0 && <span className="ml-2 text-[10px] font-sans text-gray-400 border border-gray-600 px-1 rounded">{role === 1 ? t("backend") : t("dependency")}</span>}
                </span>
                <span className="flex flex-wrap gap-x-4 gap-y-1 text-sm tabular-nums">
                    <Fact label={t("Up")} value={`${hours(w.up_minutes)} h`} extra={percent(upShare(w))} className="text-green-300" />
                    <Fact label={t("Down")} value={`${hours(downMinutes(w))} h`} className="text-gray-200" />
                    <Fact label={t("Replica-hours")} value={hours(w.replica_minutes)} className="text-gray-200" />
                    <Fact label={t("Wake-ups")} value={String(w.wakes)} className="text-gray-200" />
                </span>
            </div>
            <UptimeBars days={w.days} height={role === 0 ? 48 : 28} />
        </div>
    );
}

function Fact({ label, value, extra, className }: { label: string; value: string; extra?: string; className: string }) {
    return (
        <span>
            <span className="text-gray-500 mr-1.5">{label}</span>
            <span className={className}>{value}</span>
            {extra && <span className="text-gray-400 ml-1">({extra})</span>}
        </span>
    );
}

export function PeriodToggle({ days, onChange }: { days: number; onChange: (days: number) => void }) {
    return (
        <div className="flex gap-1 bg-gray-900 p-0.5 rounded-lg border border-gray-700" role="group" aria-label={t("Period")}>
            {[7, 30].map(n => (
                <button key={n} onClick={() => onChange(n)} aria-pressed={days === n}
                    className={`px-3 py-1 text-xs rounded-md ${days === n ? "bg-blue-600 text-white" : "text-gray-400 hover:text-white"}`}>
                    {tn(n, "{n} day", "{n} days")}
                </button>
            ))}
        </div>
    );
}
