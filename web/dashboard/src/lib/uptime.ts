import { getLang, t, tn } from "@/lib/i18n";
import type { UptimeReport, UptimeTotals } from "@/types/api";

// The browser's time zone: days are counted in it.
export const timeZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export const uptimeURL = (days: number, route?: string) =>
    `/api/uptime?${new URLSearchParams({ days: String(days), tz: timeZone(), ...(route ? { route } : {}) })}`;

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

// dayTitle describes one day, for the tooltip of its bar.
export function dayTitle(date: string, x: UptimeTotals): string {
    if (x.observed_minutes === 0) return `${dayLabel(date)}: ${t("no data")}`;
    return `${dayLabel(date)}: ${t("up {hours} h ({percent})", { hours: hours(x.up_minutes), percent: percent(upShare(x)) })}, ${t("{hours} replica-hours", { hours: hours(x.replica_minutes) })}, ${tn(x.wakes, "{n} wake-up", "{n} wake-ups")}`;
}

// sinceNote says that the history starts later than the period (just installed or upgraded).
export function sinceNote(report: UptimeReport): string | null {
    if (!report.since || !report.workloads.length) return null;
    const since = new Date(report.since);
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    start.setDate(start.getDate() - (report.days - 1));
    if (since <= start) return null;
    return t("Recorded since {when}: earlier days have no data.", { when: since.toLocaleString(getLang(), { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false }) });
}

const csvCell = (v: string | number) => (/[",\n;]/.test(String(v)) ? `"${String(v).replace(/"/g, '""')}"` : String(v));

// uptimeCSV is the report as a spreadsheet: one row per workload, totals then up hours per day.
export function uptimeCSV(report: UptimeReport): string {
    const dates = report.workloads[0]?.days.map(d => d.date) || [];
    const fixed = (n: number) => (n / 60).toFixed(2);
    const rows = [["namespace", "workload", "routes", "observed_hours", "up_hours", "down_hours", "up_percent", "replica_hours", "wakes", ...dates.map(d => `up_hours_${d}`)]];
    for (const w of report.workloads) {
        const share = upShare(w);
        rows.push([w.namespace, w.workload, w.routes.map(r => `${r.host || "*"}${r.path || ""}`).join(" "),
            fixed(w.observed_minutes), fixed(w.up_minutes), fixed(downMinutes(w)), share === null ? "" : (share * 100).toFixed(1),
            fixed(w.replica_minutes), String(w.wakes), ...w.days.map(d => fixed(d.up_minutes))]);
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
