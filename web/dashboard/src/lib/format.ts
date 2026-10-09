const NS_PER_SECOND = 1_000_000_000;

// formatDuration renders a Go duration (nanoseconds) compactly: "45s", "30m", "1h30m".
export function formatDuration(ns: number): string {
    const totalSeconds = Math.round(ns / NS_PER_SECOND);
    if (totalSeconds < 60) return `${totalSeconds}s`;
    const totalMinutes = Math.round(totalSeconds / 60);
    if (totalMinutes < 60) return `${totalMinutes}m`;
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return minutes ? `${hours}h${minutes}m` : `${hours}h`;
}

// parseDuration reads "90s", "30m", "1h", "1h30m" (a bare number means minutes).
// Returns nanoseconds, or null when the text is not a valid positive duration.
export function parseDuration(text: string): number | null {
    const value = text.trim().toLowerCase();
    if (/^\d+$/.test(value)) return Number(value) * 60 * NS_PER_SECOND;
    const match = value.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/);
    if (!match || value === "") return null;
    const [, h = "0", m = "0", s = "0"] = match;
    const seconds = Number(h) * 3600 + Number(m) * 60 + Number(s);
    return seconds > 0 ? seconds * NS_PER_SECOND : null;
}

// formatRelative renders the distance from now: "in 12m", "3h ago", "now".
export function formatRelative(iso: string, now: number): string {
    const diff = new Date(iso).getTime() - now;
    const abs = Math.abs(diff);
    if (abs < 30_000) return "now";
    const text = formatDuration(abs * 1_000_000);
    return diff > 0 ? `in ${text}` : `${text} ago`;
}

const DAY_ORDER = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const DAY_LABEL: Record<string, string> = { mon: "Mon", tue: "Tue", wed: "Wed", thu: "Thu", fri: "Fri", sat: "Sat", sun: "Sun" };

// formatSchedule renders a schedule compactly, e.g. "Mon–Fri 08:00–19:00".
export function formatSchedule(s: { days?: string[]; from: string; to: string }): string {
    const days = (s.days || []).map(d => d.toLowerCase()).sort((a, b) => DAY_ORDER.indexOf(a) - DAY_ORDER.indexOf(b));
    let label = "Every day";
    if (days.length > 0 && days.length < 7) {
        const idx = days.map(d => DAY_ORDER.indexOf(d));
        const contiguous = idx.every((v, i) => i === 0 || v === idx[i - 1] + 1);
        label = contiguous && days.length > 2
            ? `${DAY_LABEL[days[0]]}–${DAY_LABEL[days[days.length - 1]]}`
            : days.map(d => DAY_LABEL[d]).join(", ");
    }
    return `${label} ${s.from}–${s.to}`;
}

export function splitHosts(host: string): string[] {
    return host.split(",").map(h => h.trim()).filter(Boolean);
}

// workloadLabel shows a workload reference ("web" or "statefulset/db") readably.
export function workloadLabel(ref: string): string {
    const [prefix, name] = ref.includes("/") ? ref.split("/", 2) : ["", ref];
    return prefix.toLowerCase().startsWith("statefulset") || prefix.toLowerCase() === "sts" ? `${name} · StatefulSet` : name;
}
