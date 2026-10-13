import { useState } from "react";
import { ArrowDown, ArrowUp, Download, Search } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { UptimeBars } from "@/components/views/UptimeBars";
import { PeriodToggle } from "@/components/views/UptimeCard";
import { useStoredState } from "@/hooks/useStoredState";
import { usePolling } from "@/hooks/usePolling";
import { workloadLabel } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import { downMinutes, hours, percent, saveText, sinceNote, sum, upShare, uptimeCSV, uptimeURL } from "@/lib/uptime";
import type { UptimeReport, UptimeTotals, WorkloadUptime } from "@/types/api";

type SortKey = "name" | "up" | "down" | "share" | "replicas" | "wakes";

const sortValue: Record<Exclude<SortKey, "name">, (x: UptimeTotals) => number> = {
    up: x => x.up_minutes,
    down: downMinutes,
    share: x => upShare(x) ?? -1,
    replicas: x => x.replica_minutes,
    wakes: x => x.wakes,
};

// UptimeView reports, for every managed workload, how long it was up over the last 7 or 30 days,
// by namespace, with totals; downloadable as CSV.
export function UptimeView({ onSelect }: { onSelect: (routeId: string) => void }) {
    const [days, setDays] = useStoredState<number>("uptime.days", 30);
    const [sort, setSort] = useStoredState<{ key: SortKey; desc: boolean }>("uptime.sort", { key: "name", desc: false });
    const [search, setSearch] = useState("");
    const { data, error } = usePolling<UptimeReport>(uptimeURL(days), 300_000);

    if (!data) {
        return <div className="text-gray-500 text-sm py-12 text-center">{error ? t("No history: Smart Proxy isn't connected to a cluster.") : t("Loading…")}</div>;
    }

    const words = search.toLowerCase().split(/\s+/).filter(Boolean);
    const matches = (w: WorkloadUptime) => {
        const text = `${w.namespace} ${w.workload} ${w.routes.map(r => r.host + r.path).join(" ")}`.toLowerCase();
        return words.every(word => text.includes(word));
    };
    const compare = (a: WorkloadUptime, b: WorkloadUptime) => {
        const order = sort.key === "name" ? a.workload.localeCompare(b.workload) : sortValue[sort.key](a) - sortValue[sort.key](b);
        return sort.desc ? -order : order;
    };
    const shown = data.workloads.filter(matches);
    const groups = new Map<string, WorkloadUptime[]>();
    for (const w of shown) groups.set(w.namespace, [...(groups.get(w.namespace) || []), w]);
    const namespaces = [...groups.entries()]
        .map(([ns, list]) => ({ ns, list: list.sort(compare), totals: sum(list) }))
        .sort((a, b) => {
            const order = sort.key === "name" ? a.ns.localeCompare(b.ns) : sortValue[sort.key](a.totals) - sortValue[sort.key](b.totals);
            return sort.desc ? -order : order;
        });
    const total = sum(shown);
    const note = sinceNote(data);

    const toggleSort = (key: SortKey) => setSort(sort.key === key ? { key, desc: !sort.desc } : { key, desc: key !== "name" });
    const exportCSV = () => saveText(uptimeCSV({ ...data, workloads: shown }), `smart-proxy-uptime-${days}d-${new Date().toISOString().slice(0, 10)}.csv`, "text/csv");

    return (
        <div className="space-y-6">
            <div className="grid grid-cols-2 lg:grid-cols-5 gap-4">
                <Summary label={t("Workloads")} value={String(shown.length)} hint={tn(groups.size, "in {n} namespace", "in {n} namespaces")} />
                <Summary label={t("Up")} value={`${hours(total.up_minutes)} h`} hint={t("{percent} of the time", { percent: percent(upShare(total)) })} valueClass="text-green-300" />
                <Summary label={t("Asleep")} value={`${hours(downMinutes(total))} h`} hint={t("with no replica")} />
                <Summary label={t("Replica-hours")} value={hours(total.replica_minutes)} hint={t("what the replicas ran for")} />
                <Summary label={t("Wake-ups")} value={String(total.wakes)} hint={tn(days, "over the last day", "over the last {n} days")} />
            </div>

            <Card>
                <CardHeader className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                        <CardTitle>{t("Uptime of the managed workloads")}</CardTitle>
                        <p className="text-gray-400 text-sm">{t("Up means at least one replica. Days in {zone}; minutes without data (Smart Proxy not running, workload not managed yet) don't count.", { zone: data.timezone })}</p>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                        <label className="relative">
                            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-500" />
                            <input value={search} onChange={e => setSearch(e.target.value)} placeholder={t("Filter…")} aria-label={t("Filter")}
                                className="bg-gray-900 border border-gray-700 rounded-md pl-8 pr-2 py-1.5 text-sm text-gray-100 w-48" />
                        </label>
                        <PeriodToggle days={days} onChange={setDays} />
                        <Button variant="secondary" size="sm" onClick={exportCSV} disabled={shown.length === 0} className="gap-1.5">
                            <Download size={14} /> CSV
                        </Button>
                    </div>
                </CardHeader>
                <CardContent className="p-0 overflow-x-auto">
                    {data.workloads.length === 0 ? (
                        <div className="text-sm text-gray-500 italic p-6">{t("No history yet: Smart Proxy records it every minute from now on.")}</div>
                    ) : (
                        <table className="w-full text-sm">
                            <thead className="text-xs text-gray-400 border-b border-gray-700">
                                <tr>
                                    <Th label={t("Workload")} k="name" sort={sort} onSort={toggleSort} align="left" />
                                    <th className="px-3 py-2 font-medium text-left hidden md:table-cell w-[30%]">{tn(days, "Last day", "Last {n} days")}</th>
                                    <Th label={t("Up (h)")} k="up" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Down (h)")} k="down" sort={sort} onSort={toggleSort} />
                                    <Th label={t("% up")} k="share" sort={sort} onSort={toggleSort} />
                                    <Th label={t("Replica-hours")} k="replicas" sort={sort} onSort={toggleSort} />
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
                                                <div className="font-mono text-gray-100">{workloadLabel(w.workload)}</div>
                                                <div className="text-xs text-gray-500 truncate max-w-[20rem]">
                                                    {w.routes.length === 0 ? <span className="italic">{t("no longer managed")}</span> : w.routes.map((r, i) => (
                                                        <span key={r.id}>
                                                            {i > 0 && ", "}
                                                            <button className="hover:text-blue-300 hover:underline" onClick={() => onSelect(r.id)}>{(r.host || t("any host")) + (r.path && r.path !== "/" ? r.path : "")}</button>
                                                        </span>
                                                    ))}
                                                </div>
                                            </td>
                                            <td className="px-3 py-2 hidden md:table-cell"><UptimeBars days={w.days} height={24} /></td>
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
