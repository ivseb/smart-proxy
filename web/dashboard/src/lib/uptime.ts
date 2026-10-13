import { getLang, t, tn } from "@/lib/i18n";
import type { UptimeChange, UptimeDay, UptimeHour, UptimeReport, UptimeTotals, WorkloadUptime } from "@/types/api";

// The browser's time zone: days are counted in it.
export const timeZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

// Period is what the history is shown for: the last 24 hours, 7 or 30 days, or from..to
// (datetime-local values, whole hours).
export type Period = { kind: "24h" | "7d" | "30d" } | { kind: "custom"; from: string; to: string };

const HOUR = 3600_000;

// periodParams are the query parameters of a period (the end of a custom one included: to the end
// of its hour).
export function periodParams(p: Period): Record<string, string> {
    switch (p.kind) {
        case "7d": return { days: "7" };
        case "30d": return { days: "30" };
        case "24h": return { from: new Date(Math.floor((Date.now() - 23 * HOUR) / HOUR) * HOUR).toISOString() };
        default: return { from: new Date(p.from).toISOString(), to: new Date(p.to).toISOString() };
    }
}

// isShort: periods of up to 3 days are shown hour by hour, longer ones day by day.
export function isShort(p: Period): boolean {
    if (p.kind === "24h") return true;
    if (p.kind !== "custom") return false;
    return new Date(p.to).getTime() - new Date(p.from).getTime() <= 3 * 24 * HOUR;
}

export function periodTitle(p: Period, report?: UptimeReport | null): string {
    switch (p.kind) {
        case "24h": return t("Last 24 hours");
        case "7d": return t("Last {n} days", { n: 7 });
        case "30d": return t("Last {n} days", { n: 30 });
        default: return report ? `${dateTime(report.from)} → ${dateTime(report.to)}` : `${dateTime(p.from)} → ${dateTime(p.to)}`;
    }
}

export function uptimeURL(p: Period, extra: Record<string, string> = {}) {
    return `/api/uptime?${new URLSearchParams({ ...periodParams(p), tz: timeZone(), ...extra })}`;
}

const pad = (n: number) => String(n).padStart(2, "0");
// localInput is the datetime-local value of a time (minutes).
export const localInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;

// localDate is the YYYY-MM-DD date of a time, in the browser's time zone.
export const localDate = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

// clock renders a time as "03:12".
export const clock = (d: Date) => d.toLocaleTimeString(getLang(), { hour: "2-digit", minute: "2-digit", hour12: false });

// dateTime renders a time as "13 Oct, 15:00".
export const dateTime = (iso: string | Date) =>
    new Date(iso).toLocaleString(getLang(), { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false });

export const downMinutes = (x: UptimeTotals) => Math.max(0, x.observed_minutes - x.up_minutes);

// upShare is the part of the observed time with at least one replica (null: no data).
export const upShare = (x: UptimeTotals) => (x.observed_minutes > 0 ? x.up_minutes / x.observed_minutes : null);

export function sum(items: UptimeTotals[]): UptimeTotals {
    return items.reduce((a, x) => ({
        observed_minutes: a.observed_minutes + x.observed_minutes,
        up_minutes: a.up_minutes + x.up_minutes,
        replica_minutes: a.replica_minutes + x.replica_minutes,
        wakes: a.wakes + x.wakes,
    }), { observed_minutes: 0, up_minutes: 0, replica_minutes: 0, wakes: 0 });
}

// hours renders minutes as hours: "0", "0.5", "12.3", "245".
export function hours(minutes: number): string {
    const h = minutes / 60;
    return new Intl.NumberFormat(getLang(), { maximumFractionDigits: h > 0 && h < 100 ? 1 : 0 }).format(h);
}

export function percent(share: number | null): string {
    if (share === null) return "–";
    return new Intl.NumberFormat(getLang(), { style: "percent", maximumFractionDigits: share > 0 && share < 0.1 ? 1 : 0 }).format(share);
}

// dayLabel renders a YYYY-MM-DD date: "Mon 13 Oct".
export function dayLabel(date: string): string {
    const [y, m, d] = date.split("-").map(Number);
    return new Date(y, m - 1, d).toLocaleDateString(getLang(), { weekday: "short", day: "numeric", month: "short" });
}

// totalsText describes a period's totals, for a tooltip.
function totalsText(label: string, x: UptimeTotals, unit: "hours" | "minutes"): string {
    if (x.observed_minutes === 0) return `${label}: ${t("no data")}`;
    const up = unit === "hours"
        ? t("up {hours} h ({percent})", { hours: hours(x.up_minutes), percent: percent(upShare(x)) })
        : t("up {minutes} of {observed} min", { minutes: x.up_minutes, observed: x.observed_minutes });
    return `${label}: ${up}, ${t("{hours} replica-hours", { hours: hours(x.replica_minutes) })}, ${tn(x.wakes, "{n} wake-up", "{n} wake-ups")}`;
}

// dayTitle describes one day, for the tooltip of its bar.
export const dayTitle = (date: string, x: UptimeTotals) => totalsText(dayLabel(date), x, "hours");

// hourTitle describes one hour, for the tooltip of its bar or cell.
export function hourTitle(h: UptimeHour): string {
    const start = new Date(h.at);
    return totalsText(`${dayLabel(localDate(start))} ${clock(start)}–${clock(new Date(start.getTime() + HOUR))}`, h, "minutes");
}

// Segment is a stretch of time with the same replicas (null: unknown, no data).
export interface Segment {
    from: number; // ms
    to: number;
    replicas: number | null;
}

// segments rebuilds, from the changes, what the replicas were over from..to (ms). Before the
// first change (and before a gap) the state is unknown, unless a change before the period says.
export function segments(w: WorkloadUptime, from: number, to: number): Segment[] | null {
    const changes = w.changes || [];
    if (!w.before && changes.length === 0) return null; // No change recorded (before 2.7)
    const out: Segment[] = [];
    let at = from;
    let replicas: number | null = w.before ? w.before.n : null;
    for (const c of changes) {
        const t = c.t * 1000;
        if (t <= from) {
            replicas = c.n;
            continue;
        }
        if (t >= to) break;
        if (c.gap) {
            // Known until the sample before the gap, unknown from there.
            const known = c.gap_from ? Math.max(at, c.gap_from * 1000) : at;
            out.push({ from: at, to: known, replicas }, { from: known, to: t, replicas: null });
        } else {
            out.push({ from: at, to: t, replicas });
        }
        at = t;
        replicas = c.n;
    }
    out.push({ from: at, to, replicas });
    return out.filter(s => s.to > s.from);
}

// changeText says what a change was, from the one before.
export function changeText(c: UptimeChange, before: number | null): string {
    if (c.gap) return c.n > 0 ? tn(c.n, "Recording again: up, {n} replica", "Recording again: up, {n} replicas") : t("Recording again: asleep");
    if (before === 0 || before === null) return tn(c.n, "Woken: {n} replica", "Woken: {n} replicas");
    if (c.n === 0) return t("Put to sleep");
    return t("Scaled from {from} to {to} replicas", { from: before, to: c.n });
}

// hoursByDate groups a workload's hours by local date.
export function hoursByDate(list: UptimeHour[] | undefined): Map<string, UptimeHour[]> {
    const out = new Map<string, UptimeHour[]>();
    for (const h of list || []) {
        const d = localDate(new Date(h.at));
        out.set(d, [...(out.get(d) || []), h]);
    }
    return out;
}

// sinceNote says that the history starts later than the period (just installed or upgraded).
export function sinceNote(report: UptimeReport): string | null {
    if (!report.since || !report.workloads.length) return null;
    const since = new Date(report.since);
    if (since.getTime() <= Math.floor(new Date(report.from).getTime() / HOUR) * HOUR) return null;
    return t("Recorded since {when}: earlier days have no data.", { when: since.toLocaleString(getLang(), { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false }) });
}

const csvCell = (v: string | number) => (/[",\n;]/.test(String(v)) ? `"${String(v).replace(/"/g, '""')}"` : String(v));

// uptimeCSV is the report as a spreadsheet: one row per workload, totals then the hours up of
// each day (or, hour by hour, the minutes up of each hour).
export function uptimeCSV(report: UptimeReport, hourly: boolean): string {
    const fixed = (n: number) => (n / 60).toFixed(2);
    const hourKeys = [...new Set(report.workloads.flatMap(w => (w.hours || []).map(h => h.at)))].sort();
    const dates = report.workloads[0]?.days.map(d => d.date) || [];
    const columns = hourly ? hourKeys.map(h => `up_minutes_${localInput(new Date(h))}`) : dates.map(d => `up_hours_${d}`);
    const rows = [["namespace", "workload", "routes", "observed_hours", "up_hours", "down_hours", "up_percent", "replica_hours", "wakes", ...columns]];
    for (const w of report.workloads) {
        const share = upShare(w);
        rows.push([w.namespace, w.workload, w.routes.map(r => `${r.host || "*"}${r.path || ""}`).join(" "),
            fixed(w.observed_minutes), fixed(w.up_minutes), fixed(downMinutes(w)), share === null ? "" : (share * 100).toFixed(1),
            fixed(w.replica_minutes), String(w.wakes),
            ...(hourly ? hourKeys.map(k => String(w.hours?.find(h => h.at === k)?.up_minutes ?? "")) : w.days.map(d => fixed(d.up_minutes)))]);
    }
    return rows.map(r => r.map(csvCell).join(",")).join("\n") + "\n";
}

export function saveText(text: string, name: string, type: string) {
    const href = URL.createObjectURL(new Blob([text], { type }));
    const a = document.createElement("a");
    a.href = href;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(href), 10_000);
}

// canonical spells a workload reference as the history does: "web" (Deployment), "statefulset/db".
export function canonical(ref: string): string {
    const [kind, name] = ref.includes("/") ? ref.split("/", 2) : ["", ref];
    switch (kind.toLowerCase()) {
        case "": case "deployment": case "deployments": case "deploy": return name;
        case "statefulset": case "statefulsets": case "sts": return `statefulset/${name}`;
        case "deploymentconfig": case "deploymentconfigs": case "dc": return `deploymentconfig/${name}`;
        default: return ref;
    }
}

// BarItem is one bar of a chart: a day or an hour.
export interface BarItem {
    key: string;
    title: string;
    totals: UptimeTotals;
}

export const dayItems = (days: UptimeDay[]): BarItem[] => days.map(d => ({ key: d.date, title: dayTitle(d.date, d), totals: d }));
export const hourItems = (list: UptimeHour[] | undefined): BarItem[] => (list || []).map(h => ({ key: h.at, title: hourTitle(h), totals: h }));

// dayRange is the start and end (ms) of a local date, the end no later than now.
export function dayRange(date: string, now: number): [number, number] {
    const [y, m, d] = date.split("-").map(Number);
    const from = new Date(y, m - 1, d).getTime();
    return [from, Math.min(new Date(y, m - 1, d + 1).getTime(), now)];
}
