import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Download, Search, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Bars, PeriodPicker } from "@/components/views/UptimeParts";
import { UptimeHistory } from "@/components/views/UptimeHistory";
import { useStoredState } from "@/hooks/useStoredState";
import { usePolling } from "@/hooks/usePolling";
import { workloadLabel } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import {
    dayItems, downMinutes, hourItems, hours, isShort, monthItems, percent, periodTitle, savedMinutes, savedShare, saveText, sinceNote, sum, upShare,
    uptimeCSV, uptimeURL, yearCSV, type BarItem, type Period,
} from "@/lib/uptime";
import type { UptimeReport, UptimeTotals, WorkloadUptime, YearReport } from "@/types/api";

type SortKey = "name" | "up" | "down" | "share" | "replicas" | "saved" | "wakes";

const sortValue: Record<Exclude<SortKey, "name">, (x: UptimeTotals) => number> = {
    up: x => x.up_minutes,
    down: downMinutes,
    share: x => upShare(x) ?? -1,
    replicas: x => x.replica_minutes,
    saved: savedMinutes,
    wakes: x => x.wakes,
};

// Row is a workload in the table, whatever the period.
interface Row extends UptimeTotals {
    namespace: string;
    workload: string;
    routes: { id: string; host: string; path: string }[];
    bars: BarItem[];
    detail?: WorkloadUptime; // For the detail dialog (not for the year)
}

// UptimeView reports, for every managed workload, how long it was up over a period, by namespace,
// with totals; downloadable as CSV. A workload picked opens its detail.
export function UptimeView({ onSelect }: { onSelect: (routeId: string) => void }) {
    const [period, setPeriod] = useStoredState<Period>("uptime.period", { kind: "30d" });
    const [sort, setSort] = useStoredState<{ key: SortKey; desc: boolean }>("uptime.sort", { key: "name", desc: false });
    const [search, setSearch] = useState("");
    const [detail, setDetail] = useState<WorkloadUptime | null>(null);
    const short = isShort(period);
    const yearly = period.kind === "year";
    const { data, error } = usePolling<UptimeReport | YearReport>(yearly ? "/api/uptime/year" : uptimeURL(period, short ? { hours: "1" } : {}), 120_000);

    // The answer of the call before the period changed: wait for the new one.
    if (!data || ("months" in data) !== yearly) {
        return <div className="text-gray-500 text-sm py-12 text-center">{error ? t("No history: Smart Proxy isn't connected to a cluster.") : t("Loading…")}</div>;
    }
    const year = "months" in data ? data : null;
    const report = "months" in data ? null : data;
    const rows: Row[] = year
        ? year.workloads.map(w => ({ ...w, bars: monthItems(w.months) }))
        : report!.workloads.map(w => ({ ...w, bars: short ? hourItems(w.hours) : dayItems(w.days), detail: w }));

    const words = search.toLowerCase().split(/\s+/).filter(Boolean);
    const matches = (w: Row) => {
        const text = `${w.namespace} ${w.workload} ${w.routes.map(r => r.host + r.path).join(" ")}`.toLowerCase();
        return words.every(word => text.includes(word));
    };
    const compare = (a: Row, b: Row) => {
        const order = sort.key === "name" ? a.workload.localeCompare(b.workload) : sortValue[sort.key](a) - sortValue[sort.key](b);
        return sort.desc ? -order : order;
    };
    const shown = rows.filter(matches);
    const groups = new Map<string, Row[]>();
    for (const w of shown) groups.set(w.namespace, [...(groups.get(w.namespace) || []), w]);
    const namespaces = [...groups.entries()]
        .map(([ns, list]) => ({ ns, list: list.sort(compare), totals: sum(list) }))
        .sort((a, b) => {
            const order = sort.key === "name" ? a.ns.localeCompare(b.ns) : sortValue[sort.key](a.totals) - sortValue[sort.key](b.totals);
            return sort.desc ? -order : order;
        });
    const total = sum(shown);
    const note = report ? sinceNote(report) : year?.since && year.workloads.length && new Date(year.since) > new Date(`${year.months[0]}-01`)
        ? t("Recorded since {when}: earlier months have no data.", { when: new Date(year.since).toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" }) }) : null;

    const toggleSort = (key: SortKey) => setSort(sort.key === key ? { key, desc: !sort.desc } : { key, desc: key !== "name" });
    const exportCSV = () => year
        ? saveText(yearCSV(year, year.workloads.filter(w => shown.some(r => r.namespace === w.namespace && r.workload === w.workload))), `smart-proxy-uptime-12-months-${year.months[11]}.csv`, "text/csv")
        : saveText(uptimeCSV({ ...report!, workloads: shown.map(r => r.detail!) }, short), `smart-proxy-uptime-${report!.from.slice(0, 13)}-${report!.to.slice(0, 13)}.csv`.replace(/:/g, ""), "text/csv");
    const title = year ? t("Last 12 months") : periodTitle(period, report);

    return (
        <div className="space-y-6">
            <div className="grid grid-cols-2 lg:grid-cols-3 xl:grid-cols-6 gap-4">
                <Summary label={t("Workloads")} value={String(shown.length)} hint={tn(groups.size, "in {n} namespace", "in {n} namespaces")} />
                <Summary label={t("Up")} value={`${hours(total.up_minutes)} h`} hint={t("{percent} of the time", { percent: percent(upShare(total)) })} valueClass="text-green-300" />
                <Summary label={t("Asleep")} value={`${hours(downMinutes(total))} h`} hint={t("with no replica")} />
                <Summary label={t("Replica-hours")} value={hours(total.replica_minutes)} hint={t("what the replicas ran for")} />
                <Summary label={t("Replica-hours saved")} value={hours(savedMinutes(total))} valueClass="text-emerald-300"
                    hint={t("{percent} of {hours} always on", { percent: percent(savedShare(total)), hours: hours(total.baseline_minutes || 0) })} />
                <Summary label={t("Wake-ups")} value={String(total.wakes)} hint={title} />
            </div>

            <Card>
                <CardHeader className="flex flex-wrap items-start justify-between gap-3">
                    <div className="max-w-2xl">
                        <CardTitle>{t("Uptime of the managed workloads")} · {title}</CardTitle>
                        <p className="text-gray-400 text-sm">{t("Up means at least one replica. Days in {zone}; minutes without data (Smart Proxy not running, workload not managed yet) don't count.", { zone: data.timezone })}</p>
                        <p className="text-gray-500 text-xs mt-1">{t("Saved: the replica-hours each workload would have used always on (with the replicas it has when awake), minus those it used.")}</p>
                    </div>
                    <div className="flex flex-col items-end gap-2">
                    <PeriodPicker value={period} onChange={setPeriod} withYear />
                    <div className="flex flex-wrap items-center gap-2">
                        <label className="relative">
                            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-500" />
                            <input value={search} onChange={e => setSearch(e.target.value)} placeholder={t("Filter…")} aria-label={t("Filter")}
                                className="bg-gray-900 border border-gray-700 rounded-md pl-8 pr-2 py-1.5 text-sm text-gray-100 w-48" />
                        </label>
                        <Button variant="secondary" size="sm" onClick={exportCSV} disabled={shown.length === 0} className="gap-1.5">
                            <Download size={14} /> CSV
                        </Button>
                    </div>
                    </div>
                </CardHeader>
                <CardContent className="p-0 overflow-x-auto">
                    {rows.length === 0 ? (
                        <div className="text-sm text-gray-500 italic p-6">{t("No history yet: Smart Proxy records it every minute from now on.")}</div>
                    ) : (
                        <table className="w-full text-sm">
                            <thead className="text-xs text-gray-400 border-b border-gray-700">
                                <tr>
                                    <Th label={t("Workload")} k="name" sort={sort} onSort={toggleSort} align="left" />
                                    <th className="px-3 py-2 font-medium text-left hidden md:table-cell w-[28%]">{year ? t("Month by month") : short ? t("Hour by hour") : t("Day by day")}</th>
                                    <Th label={t("Up (h)")} k="up" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Down (h)")} k="down" sort={sort} onSort={toggleSort} />
                                    <Th label={t("% up")} k="share" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Replica-hours")} k="replicas" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Saved")} k="saved" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Wake-ups")} k="wakes" sort={sort} onSort={toggleSort} />
                                </tr>
                            </thead>
                            {namespaces.map(({ ns, list, totals }) => (
                                <tbody key={ns} className="border-b border-gray-700 last:border-0">
                                    <tr className="bg-gray-900/50 text-gray-300">
                                        <td className="px-4 py-2 font-medium" colSpan={2}>{ns} <span className="text-gray-500 font-normal">· {tn(list.length, "{n} workload", "{n} workloads")}</span></td>
                                        <Numbers x={totals} />
                                    </tr>
                                    {list.map(w => (
                                        <tr key={w.workload} className="border-t border-gray-800 hover:bg-white/[0.02]">
                                            <td className="px-4 py-2 min-w-[12rem]">
                                                {w.detail ? (
                                                    <button className="font-mono text-gray-100 hover:text-blue-300 hover:underline text-left" onClick={() => setDetail(w.detail!)}
                                                        title={t("When exactly it was up")}>{workloadLabel(w.workload)}</button>
                                                ) : <span className="font-mono text-gray-100">{workloadLabel(w.workload)}</span>}
                                                <div className="text-xs text-gray-500 truncate max-w-[20rem]">
                                                    {w.routes.length === 0 ? <span className="italic">{t("no longer managed")}</span> : w.routes.map((r, i) => (
                                                        <span key={r.id}>
                                                            {i > 0 && ", "}
                                                            <button className="hover:text-blue-300 hover:underline" onClick={() => onSelect(r.id)}>{(r.host || t("any host")) + (r.path && r.path !== "/" ? r.path : "")}</button>
                                                        </span>
                                                    ))}
                                                </div>
                                            </td>
                                            <td className="px-3 py-2 hidden md:table-cell"><Bars items={w.bars} height={24} /></td>
                                            <Numbers x={w} />
                                        </tr>
                                    ))}
                                </tbody>
                            ))}
                        </table>
                    )}
                </CardContent>
            </Card>
            {note && <p className="text-xs text-gray-500">{note}</p>}
            {detail && <WorkloadDialog w={detail} period={period} onClose={() => setDetail(null)} />}
        </div>
    );
}

// WorkloadDialog shows one workload over the period, to the minute.
function WorkloadDialog({ w, period, onClose }: { w: WorkloadUptime; period: Period; onClose: () => void }) {
    const { data } = usePolling<UptimeReport>(uptimeURL(period, { workload: `${w.namespace}/${w.workload}` }), 120_000);
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    return (
        <div className="fixed inset-0 z-50 flex items-start sm:items-center justify-center p-4 bg-black/50 backdrop-blur-sm overflow-y-auto" role="dialog" aria-modal="true" onClick={onClose}>
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-4xl" onClick={e => e.stopPropagation()}>
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-start gap-3">
                    <div className="min-w-0">
                        <h2 className="text-lg font-bold text-white truncate">{w.namespace} / {workloadLabel(w.workload)}</h2>
                        <p className="text-sm text-gray-400">{periodTitle(period, data)}</p>
                    </div>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label={t("Close")}><X className="w-6 h-6" /></button>
                </div>
                <div className="p-6">
                    {!data ? <div className="text-sm text-gray-500">{t("Loading…")}</div>
                        : data.workloads.length === 0 ? <div className="text-sm text-gray-500 italic">{t("No data in this period")}</div>
                        : <UptimeHistory report={data} period={period} />}
                </div>
            </div>
        </div>
    );
}

function Numbers({ x }: { x: UptimeTotals }) {
    return (
        <>
            <td className="px-3 py-2 text-right tabular-nums text-green-300">{hours(x.up_minutes)}</td>
            <td className="px-3 py-2 text-right tabular-nums">{hours(downMinutes(x))}</td>
            <td className="px-3 py-2 text-right tabular-nums">{percent(upShare(x))}</td>
            <td className="px-3 py-2 text-right tabular-nums">{hours(x.replica_minutes)}</td>
            <td className="px-3 py-2 text-right tabular-nums text-emerald-300" title={t("{percent} of {hours} always on", { percent: percent(savedShare(x)), hours: hours(x.baseline_minutes || 0) })}>{hours(savedMinutes(x))}</td>
            <td className="px-3 py-2 text-right tabular-nums">{x.wakes}</td>
        </>
    );
}

function Th({ label, k, sort, onSort, align = "right" }: { label: string; k: SortKey; sort: { key: SortKey; desc: boolean }; onSort: (k: SortKey) => void; align?: "left" | "right" }) {
    const active = sort.key === k;
    return (
        <th className={`px-3 py-2 font-medium whitespace-nowrap ${align === "left" ? "text-left pl-4" : "text-right"}`} aria-sort={active ? (sort.desc ? "descending" : "ascending") : "none"}>
            <button onClick={() => onSort(k)} className={`inline-flex items-center gap-1 hover:text-white ${active ? "text-white" : ""}`}>
                {label}
                {active && (sort.desc ? <ArrowDown size={12} /> : <ArrowUp size={12} />)}
            </button>
        </th>
    );
}

function Summary({ label, value, hint, valueClass = "text-white" }: { label: string; value: string; hint: string; valueClass?: string }) {
    return (
        <Card>
            <CardContent className="p-4 space-y-1">
                <div className="text-xs text-gray-400">{label}</div>
                <div className={`text-2xl font-semibold tabular-nums ${valueClass}`}>{value}</div>
                <div className="text-xs text-gray-500">{hint}</div>
            </CardContent>
        </Card>
    );
}
