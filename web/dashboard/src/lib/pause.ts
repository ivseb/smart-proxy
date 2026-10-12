import type { Pause } from "@/types/api";
import { getLang, t } from "@/lib/i18n";

// pauseActive reports whether a route's pause is on.
export function pauseActive(pause: Pause | null | undefined, now: number): pause is Pause {
    return !!pause && (!pause.until || new Date(pause.until).getTime() > now);
}

// whenText says a time compactly: "18:00", "tomorrow 08:00", "Fri 14 Nov 09:00".
export function whenText(iso: string, now: number): string {
    const at = new Date(iso);
    const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const time = at.toLocaleTimeString(getLang(), { hour: "2-digit", minute: "2-digit", hour12: false });
    const days = Math.round((day(at) - day(new Date(now))) / 86_400_000);
    if (days === 0) return time;
    if (days === 1) return t("tomorrow {time}", { time });
    return `${at.toLocaleDateString(getLang(), { weekday: "short", day: "numeric", month: "short" })} ${time}`;
}

// pausedUntil says until when a route is paused: "Paused until 18:00", "Paused until tomorrow 08:00".
export function pausedUntil(iso: string, now: number): string {
    const sameDay = new Date(iso).toDateString() === new Date(now).toDateString();
    return sameDay ? t("Paused until {time}", { time: whenText(iso, now) }) : t("Paused until {day}", { day: whenText(iso, now) });
}

// pauseText describes a pause: "Paused until 18:00", "Paused until resumed".
export function pauseText(pause: Pause, now: number): string {
    return pause.until ? pausedUntil(pause.until, now) : t("Paused until resumed");
}

// Ready-made pause lengths; tomorrow's start of the working day is computed when chosen.
export function tomorrowMorning(now: number): Date {
    const d = new Date(now);
    d.setDate(d.getDate() + 1);
    d.setHours(8, 0, 0, 0);
    return d;
}
