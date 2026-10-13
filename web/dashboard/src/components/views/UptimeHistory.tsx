import { useState } from "react";
import { workloadLabel } from "@/lib/format";
import { t } from "@/lib/i18n";
import { dayItems, isShort, sinceNote, type Period } from "@/lib/uptime";
import type { UptimeReport, WorkloadUptime } from "@/types/api";
import { Bars, DayDetail, Facts, Heatmap, Legend, WorkloadPeriod } from "@/components/views/UptimeParts";

// UptimeHistory shows some workloads over a period (the first one in more detail): short periods
// to the minute, long ones day by day with a map of every hour; a day picked opens its detail.
export function UptimeHistory({ report, period, roles }: { report: UptimeReport; period: Period; roles?: (w: WorkloadUptime) => string | null }) {
    const [day, setDay] = useState<string | null>(null);
    const workloads = report.workloads;
    const short = isShort(period);
    const from = new Date(report.from).getTime();
    const to = new Date(report.to).getTime();
    const dates = workloads[0]?.days.map(d => d.date) || [];
    const note = sinceNote(report);
    const pick = (date: string) => setDay(day === date ? null : date);

    return (
        <div className="space-y-5">
            {workloads.map((w, i) => (
                <div key={w.workload} className="space-y-2">
                    {short ? (
                        <WorkloadPeriod w={w} from={from} to={to} role={roles?.(w) ?? null} facts />
                    ) : (
                        <>
                            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                                <span className="font-mono text-sm text-gray-100">
                                    {workloadLabel(w.workload)}
                                    {roles?.(w) && <span className="ml-2 text-[10px] font-sans text-gray-400 border border-gray-600 px-1 rounded">{roles(w)}</span>}
                                </span>
                                <Facts w={w} />
                            </div>
                            <Bars items={dayItems(w.days)} height={i === 0 ? 48 : 28} selected={day} onSelect={pick} />
                            {i === 0 && w.hours && w.hours.length > 0 && (
                                <div className="pt-1 space-y-1">
                                    <div className="text-xs text-gray-400">{t("Hour by hour: a column per day, midnight at the top. Pick a day for its detail.")}</div>
                                    <Heatmap w={w} dates={dates} selected={day} onSelect={pick} />
                                </div>
                            )}
                        </>
                    )}
                </div>
            ))}
            {!short && day && <DayDetail date={day} workloads={workloads} roles={roles} onClose={() => setDay(null)} />}
            {short && <Legend />}
            {note && <p className="text-xs text-gray-500">{note}</p>}
        </div>
    );
}
