import { useState } from "react";
import { X } from "lucide-react";
import { useNow } from "@/hooks/useNow";
import { workloadLabel } from "@/lib/format";
import { t, tn } from "@/lib/i18n";
import {
    changeText, clock, dateTime, dayLabel, dayRange, downMinutes, hourItems, hours, hourTitle, hoursByDate, localDate, localInput, percent,
    segments, upShare, type BarItem, type Period, type Segment,
} from "@/lib/uptime";
import type { UptimeTotals, WorkloadUptime } from "@/types/api";

const HOUR = 3600_000;


// Bars draws one bar per day or hour: its height is the share of it with at least one replica.
export function Bars({ items, height = 40, selected, onSelect }: { items: BarItem[]; height?: number; selected?: string | null; onSelect?: (key: string) => void }) {
    return (
        <div className="flex items-end gap-px" style={{ height }}>
            {items.map(d => {
                const share = upShare(d.totals);
                return (
                    <div key={d.key} title={d.title} onClick={onSelect ? () => onSelect(d.key) : undefined}
                        className={`flex-1 min-w-[2px] h-full flex items-end rounded-sm ${selected === d.key ? "bg-blue-500/30 ring-1 ring-blue-400" : "bg-gray-700/40"} ${onSelect ? "cursor-pointer hover:bg-gray-600/60" : ""}`}>
                        {share === null ? (
                            <div className="w-full h-px bg-gray-600" />
                        ) : (
                            <div className={`w-full rounded-sm ${share > 0 ? "bg-green-500/80" : ""}`} style={{ height: `${Math.max(share > 0 ? 6 : 0, share * 100)}%` }} />
                        )}
                    </div>
                );
            })}
        </div>
    );
}

const UNKNOWN = "repeating-linear-gradient(135deg, rgba(156,163,175,.25) 0 3px, transparent 3px 7px)";

function segmentTitle(s: Segment, multiDay: boolean): string {
    const when = `${multiDay ? dateTime(new Date(s.from)) : clock(new Date(s.from))}–${clock(new Date(s.to))}`;
    if (s.replicas === null) return `${when}: ${t("no data")}`;
    return `${when}: ${s.replicas > 0 ? tn(s.replicas, "up, {n} replica", "up, {n} replicas") : t("asleep")}`;
}

// Timeline draws, to the minute, when a workload had replicas over from..to (ms).
export function Timeline({ segs, from, to, height = 20 }: { segs: Segment[]; from: number; to: number; height?: number }) {
    const span = to - from;
    const multiDay = span > 26 * HOUR;
    // Ticks on round hours: every hour for a few hours, up to every 12 hours for days.
    const step = [1, 2, 3, 6, 12].map(h => h * HOUR).find(st => span / st <= 6) || 24 * HOUR;
    const first = new Date(from);
    first.setMinutes(0, 0, 0);
    while (first.getTime() < from || first.getHours() % (step / HOUR) !== 0) first.setTime(first.getTime() + HOUR);
    const ticks: number[] = [];
    for (let at = first.getTime(); at <= to; at += step) ticks.push(at);
    return (
        <div>
            <div className="relative w-full rounded-sm overflow-hidden bg-gray-700/40" style={{ height }}>
                {segs.map(s => (
                    <div key={s.from} title={segmentTitle(s, multiDay)} className="absolute top-0 bottom-0"
                        style={{
                            left: `${((s.from - from) / span) * 100}%`, width: `${((s.to - s.from) / span) * 100}%`,
                            background: s.replicas === null ? UNKNOWN : s.replicas > 0 ? "rgba(34,197,94,.85)" : "rgba(55,65,81,.9)",
                        }} />
                ))}
            </div>
            <div className="relative h-4 text-[10px] text-gray-500 mt-0.5 tabular-nums">
                {ticks.map(at => (
                    <span key={at} className="absolute -translate-x-1/2 whitespace-nowrap" style={{ left: `${((at - from) / span) * 100}%` }}>
                        {multiDay && new Date(at).getHours() === 0 ? dayLabel(localDate(new Date(at))) : clock(new Date(at))}
                    </span>
                ))}
            </div>
        </div>
    );
}

export function Legend() {
    return (
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-400">
            <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm bg-green-500/85" /> {t("up")}</span>
            <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm bg-gray-700" /> {t("asleep")}</span>
            <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm" style={{ background: UNKNOWN }} /> {t("no data")}</span>
        </div>
    );
}

// Heatmap shows every hour of the period: a column per day, a row per hour of the day (00 at
// the top), brighter the longer it was up. A night asleep is a dark band.
export function Heatmap({ w, dates, selected, onSelect }: { w: WorkloadUptime; dates: string[]; selected?: string | null; onSelect?: (date: string) => void }) {
    const now = useNow(60_000);
    const byDate = hoursByDate(w.hours);
    const future = (date: string, hour: number) => {
        const [y, m, d] = date.split("-").map(Number);
        return new Date(y, m - 1, d, hour).getTime() > now;
    };
    const cell = (date: string, hour: number) => byDate.get(date)?.find(h => new Date(h.at).getHours() === hour);
    return (
        <div className="flex gap-1.5">
            <div className="flex flex-col justify-between text-[10px] text-gray-500 tabular-nums py-px">
                {["00", "06", "12", "18", "24"].map(h => <span key={h} className="leading-none">{h}</span>)}
            </div>
            <div className="flex-1 flex gap-px" role="grid" aria-label={t("Hours of each day")}>
                {dates.map(date => (
                    <div key={date} onClick={onSelect ? () => onSelect(date) : undefined}
                        className={`flex-1 min-w-[4px] flex flex-col gap-px rounded-sm ${selected === date ? "ring-1 ring-blue-400" : ""} ${onSelect ? "cursor-pointer hover:opacity-80" : ""}`}>
                        {Array.from({ length: 24 }, (_, hour) => {
                            const h = cell(date, hour);
                            const share = h ? upShare(h) : null;
                            if (!h && future(date, hour)) return <div key={hour} className="h-[7px]" />;
                            return (
                                <div key={hour} title={h ? hourTitle(h) : `${dayLabel(date)} ${String(hour).padStart(2, "0")}:00: ${t("no data")}`}
                                    className="h-[7px] rounded-[1px]"
                                    style={{ background: share === null ? UNKNOWN : share === 0 ? "rgba(55,65,81,.9)" : `rgba(34,197,94,${0.3 + 0.65 * share})` }} />
                            );
                        })}
                    </div>
                ))}
            </div>
        </div>
    );
}

// Changes lists when the replicas changed over from..to (ms), with the state at its start.
export function Changes({ w, from, to, showName }: { w: WorkloadUptime; from: number; to: number; showName?: boolean }) {
    const list = (w.changes || []).filter(c => c.t * 1000 >= from && c.t * 1000 < to);
    const startState = [...(w.changes || [])].reverse().find(c => c.t * 1000 < from) || w.before;
    const multiDay = to - from > 26 * HOUR;
    const when = (ms: number) => (multiDay ? dateTime(new Date(ms)) : clock(new Date(ms)));
    const name = showName ? <span className="font-mono text-gray-300 mr-2">{workloadLabel(w.workload)}</span> : null;
    return (
        <ul className="space-y-1 text-sm">
            <li className="text-gray-400">
                {name}<span className="tabular-nums text-gray-500 mr-2">{when(from)}</span>
                {startState ? (startState.n > 0 ? tn(startState.n, "up, {n} replica", "up, {n} replicas") : t("asleep")) : t("no data")}
            </li>
            {list.map((c, i) => {
                const text = changeText(c, i > 0 ? list[i - 1].n : startState ? startState.n : null);
                return (
                    <li key={c.t} className={c.n > 0 ? "text-green-300" : "text-gray-300"}>
                        {name}<span className="tabular-nums text-gray-500 mr-2">{when(c.t * 1000)}</span>{text}
                        {c.gap && c.gap_from ? <span className="text-gray-500"> · {t("no data since {time}", { time: when(c.gap_from * 1000) })}</span> : null}
                    </li>
                );
            })}
            {list.length === 0 && <li className="text-gray-500 italic">{t("No change in this period")}</li>}
        </ul>
    );
}

export function PeriodPicker({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
    const [custom, setCustom] = useState(() => value.kind === "custom" ? value : (() => {
        const to = new Date(Math.ceil(Date.now() / HOUR) * HOUR);
        return { kind: "custom" as const, from: localInput(new Date(to.getTime() - 24 * HOUR)), to: localInput(to) };
    })());
    const [editing, setEditing] = useState(false);
    const now = useNow(60_000);
    const invalid = !custom.from || !custom.to || new Date(custom.to) <= new Date(custom.from) || new Date(custom.from).getTime() < now - 30 * 24 * HOUR - HOUR;
    const presets: { kind: "24h" | "7d" | "30d"; label: string }[] = [
        { kind: "24h", label: t("24 hours") }, { kind: "7d", label: tn(7, "{n} day", "{n} days") }, { kind: "30d", label: tn(30, "{n} day", "{n} days") },
    ];
    const button = (active: boolean) => `px-3 py-1 text-xs rounded-md whitespace-nowrap ${active ? "bg-blue-600 text-white" : "text-gray-400 hover:text-white"}`;
    return (
        <div className="flex flex-col items-end gap-2">
            <div className="flex gap-1 bg-gray-900 p-0.5 rounded-lg border border-gray-700" role="group" aria-label={t("Period")}>
                {presets.map(p => (
                    <button key={p.kind} onClick={() => { setEditing(false); onChange({ kind: p.kind }); }} aria-pressed={value.kind === p.kind} className={button(value.kind === p.kind)}>{p.label}</button>
                ))}
                <button onClick={() => setEditing(!editing)} aria-pressed={value.kind === "custom"} className={button(value.kind === "custom")}>{t("From… to…")}</button>
            </div>
            {editing && (
                <div className="flex flex-wrap items-center justify-end gap-2 text-xs">
                    <input type="datetime-local" step={3600} value={custom.from} onChange={e => setCustom({ ...custom, from: e.target.value })} aria-label={t("From")}
                        className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-gray-100" />
                    <span className="text-gray-500">→</span>
                    <input type="datetime-local" step={3600} value={custom.to} onChange={e => setCustom({ ...custom, to: e.target.value })} aria-label={t("To")}
                        className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-gray-100" />
                    <button disabled={invalid} onClick={() => { setEditing(false); onChange(custom); }}
                        className="px-3 py-1 rounded-md bg-blue-600 text-white disabled:opacity-40">{t("Show")}</button>
                </div>
            )}
        </div>
    );
}

export function Facts({ w }: { w: UptimeTotals }) {
    return (
        <span className="flex flex-wrap gap-x-4 gap-y-1 text-sm tabular-nums">
            <Fact label={t("Up")} value={`${hours(w.up_minutes)} h`} extra={percent(upShare(w))} className="text-green-300" />
            <Fact label={t("Down")} value={`${hours(downMinutes(w))} h`} className="text-gray-200" />
            <Fact label={t("Replica-hours")} value={hours(w.replica_minutes)} className="text-gray-200" />
            <Fact label={t("Wake-ups")} value={String(w.wakes)} className="text-gray-200" />
        </span>
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


// DayDetail shows one day of some workloads: each one's timeline to the minute (hour by hour
// when no change was recorded, before 2.7) and when their replicas changed.
export function DayDetail({ date, workloads, roles, onClose }: { date: string; workloads: WorkloadUptime[]; roles?: (w: WorkloadUptime) => string | null; onClose: () => void }) {
    const now = useNow(60_000);
    const [from, to] = dayRange(date, now);
    return (
        <div className="rounded-lg border border-blue-500/30 bg-gray-900/60 p-4 space-y-4">
            <div className="flex items-center justify-between gap-3">
                <h4 className="font-medium text-white">{dayLabel(date)}</h4>
                <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label={t("Close")}><X size={18} /></button>
            </div>
            {workloads.map(w => <WorkloadPeriod key={w.workload} w={w} from={from} to={to} role={roles?.(w) ?? null} date={date} />)}
            <Legend />
        </div>
    );
}

// WorkloadPeriod is a workload over a short period: its timeline (or hourly bars) and its changes.
export function WorkloadPeriod({ w, from, to, role, date, facts }: { w: WorkloadUptime; from: number; to: number; role: string | null; date?: string; facts?: boolean }) {
    const segs = segments(w, from, to);
    const hoursIn = (w.hours || []).filter(h => (date ? localDate(new Date(h.at)) === date : true));
    return (
        <div className="space-y-2">
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <span className="text-sm font-mono text-gray-100">
                    {workloadLabel(w.workload)}
                    {role && <span className="ml-2 text-[10px] font-sans text-gray-400 border border-gray-600 px-1 rounded">{role}</span>}
                </span>
                {facts && <Facts w={w} />}
            </div>
            {segs ? <Timeline segs={segs} from={from} to={to} /> : <Bars items={hourItems(hoursIn)} height={28} />}
            {segs ? <Changes w={w} from={from} to={to} /> : <p className="text-xs text-gray-500">{t("Hour by hour: the exact times are recorded from version 2.7 on.")}</p>}
        </div>
    );
}
