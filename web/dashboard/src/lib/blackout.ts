import { getLang, msg, t } from "@/lib/i18n";
import type { BlackoutPolicy, BlackoutWindow, BlockedState, Holiday } from "@/types/api";

export const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
export const DAY_SHORT: Record<string, string> = { mon: msg("Mon"), tue: msg("Tue"), wed: msg("Wed"), thu: msg("Thu"), fri: msg("Fri"), sat: msg("Sat"), sun: msg("Sun") };
// One letter per day (M T W T F S S, L M M G V S D), for compact toggles.
export const letter = (day: string) => t(DAY_SHORT[day]).slice(0, 1);

// daysText renders days compactly: "Mon–Fri", "Sat, Sun", "Every day".
export function daysText(days?: string[]): string {
    const idx = (days || []).map(d => DAYS.indexOf(d as typeof DAYS[number])).filter(i => i >= 0).sort((a, b) => a - b);
    if (idx.length === 0 || idx.length === 7) return t("Every day");
    const runs: number[][] = [];
    for (const i of idx) {
        const last = runs[runs.length - 1];
        if (last && last[last.length - 1] === i - 1) last.push(i);
        else runs.push([i]);
    }
    return runs.map(r => r.length > 2 ? `${t(DAY_SHORT[DAYS[r[0]]])}–${t(DAY_SHORT[DAYS[r[r.length - 1]]])}` : r.map(i => t(DAY_SHORT[DAYS[i]])).join(", ")).join(", ");
}

export const allDay = (w: BlackoutWindow) => w.from === "00:00" && w.to === "24:00";

// windowText renders a window: "Mon–Fri 22:00–06:00", "Sat, Sun all day".
export function windowText(w: BlackoutWindow): string {
    return `${daysText(w.days)} ${allDay(w) ? t("all day") : `${w.from}–${w.to}`}`;
}

export function policyText(p?: BlackoutPolicy | null): string {
    if (!p || p.mode !== "custom" || !p.windows?.length) return t("No blocked hours");
    return p.windows.map(windowText).join(" · ");
}

// whenText renders a coming time: "today 22:00", "tomorrow 06:00", "Mon 06:00", "12 Oct 06:00".
export function whenText(iso: string, now: number): string {
    const d = new Date(iso);
    const time = d.toLocaleTimeString(getLang(), { hour: "2-digit", minute: "2-digit", hour12: false });
    const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const days = Math.round((day(d) - day(new Date(now))) / 86_400_000);
    if (days === 0) return t("today {time}", { time });
    if (days === 1) return t("tomorrow {time}", { time });
    if (days > 1 && days < 7) return `${d.toLocaleDateString(getLang(), { weekday: "short" })} ${time}`;
    return `${d.toLocaleDateString(getLang(), { day: "numeric", month: "short" })} ${time}`;
}

// stateText says, in a few words, whether the route can be woken now and until when.
export function stateText(s: BlockedState | undefined, now: number): string | null {
    if (!s || s.policy.mode !== "custom") return s?.exception ? exceptionText(s.exception.kind, s.exception.to, now) : null;
    if (s.exception) return exceptionText(s.exception.kind, s.exception.to, now);
    if (s.blocked) return s.change ? t("Blocked until {when}", { when: whenText(s.change, now) }) : t("Blocked");
    return s.change ? t("Blocked from {when}", { when: whenText(s.change, now) }) : null;
}

export function exceptionText(kind: "allow" | "on", to: string, now: number): string {
    return kind === "on" ? t("Kept on until {when}", { when: whenText(to, now) }) : t("Can be woken until {when}", { when: whenText(to, now) });
}

// Easter Sunday (Gregorian), for Easter Monday.
function easter(year: number): Date {
    const a = year % 19, b = Math.floor(year / 100), c = year % 100, d = Math.floor(b / 4), e = b % 4;
    const f = Math.floor((b + 8) / 25), g = Math.floor((b - f + 1) / 3), h = (19 * a + b - d - g + 15) % 30;
    const i = Math.floor(c / 4), k = c % 4, l = (32 + 2 * e + 2 * i - h - k) % 7, m = Math.floor((a + 11 * h + 22 * l) / 451);
    const month = Math.floor((h + l - 7 * m + 114) / 31), day = ((h + l - 7 * m + 114) % 31) + 1;
    return new Date(year, month - 1, day);
}

const pad = (n: number) => String(n).padStart(2, "0");

// italianHolidays are Italy's public holidays: the fixed ones every year, Easter Monday for the
// given years.
export function italianHolidays(years: number[]): Holiday[] {
    const fixed: Holiday[] = [
        { date: "01-01", name: "Capodanno" }, { date: "01-06", name: "Epifania" }, { date: "04-25", name: "Liberazione" },
        { date: "05-01", name: "Festa del lavoro" }, { date: "06-02", name: "Festa della Repubblica" }, { date: "08-15", name: "Ferragosto" },
        { date: "11-01", name: "Ognissanti" }, { date: "12-08", name: "Immacolata" }, { date: "12-25", name: "Natale" }, { date: "12-26", name: "Santo Stefano" },
    ];
    const mondays = years.map(y => {
        const e = easter(y);
        const m = new Date(e.getFullYear(), e.getMonth(), e.getDate() + 1);
        return { date: `${m.getFullYear()}-${pad(m.getMonth() + 1)}-${pad(m.getDate())}`, name: "Pasquetta" };
    });
    return [...fixed, ...mondays];
}

// holidayText renders a holiday's date: "25 Dec, every year", "6 Apr 2026".
export function holidayText(h: Holiday): string {
    const parts = h.date.split("-").map(Number);
    if (parts.length === 2) {
        const d = new Date(2000, parts[0] - 1, parts[1]);
        return t("{date}, every year", { date: d.toLocaleDateString(getLang(), { day: "numeric", month: "long" }) });
    }
    return new Date(parts[0], parts[1] - 1, parts[2]).toLocaleDateString(getLang(), { day: "numeric", month: "long", year: "numeric" });
}

// sortHolidays orders holidays by month and day, one-off dates by year too.
export function sortHolidays(list: Holiday[]): Holiday[] {
    const key = (h: Holiday) => (h.date.length === 5 ? `0000-${h.date}` : h.date).slice(5) + h.date;
    return [...list].sort((a, b) => key(a).localeCompare(key(b)));
}

export const PRESETS: { label: string; windows: BlackoutWindow[] }[] = [
    { label: msg("Weeknights 22–06"), windows: [{ days: ["mon", "tue", "wed", "thu", "fri"], from: "22:00", to: "06:00" }] },
    { label: msg("Whole weekend"), windows: [{ days: ["sat", "sun"], from: "00:00", to: "24:00" }] },
];

// windowError says what's wrong with a window, if anything.
export function windowError(w: BlackoutWindow): string | null {
    if (!/^\d\d:\d\d$/.test(w.from) || !/^(\d\d:\d\d|24:00)$/.test(w.to)) return t("Set both times");
    if (w.from === w.to) return t("The start and the end must differ");
    return null;
}

export const windowsValid = (windows: BlackoutWindow[]) => windows.length > 0 && windows.every(w => !windowError(w));
