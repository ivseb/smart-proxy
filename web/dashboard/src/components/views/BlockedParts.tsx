import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { getLang, t } from "@/lib/i18n";
import { allDay, DAY_SHORT, DAYS, daysText, letter, PRESETS, windowError } from "@/lib/blackout";
import type { BlackoutPolicy, BlackoutWindow, BlockedHoursView } from "@/types/api";


// nightText explains a window whose end is earlier than its start: the night hours of each day.
function nightText(w: BlackoutWindow): string | null {
    if (allDay(w) || w.to >= w.from || w.to === "00:00") return null;
    return t("On {days}: from 00:00 to {to} and from {from} to 24:00", { days: daysText(w.days), from: w.from, to: w.to });
}

// WindowsEditor edits a list of blocked windows: days, and hours or the whole day.
export function WindowsEditor({ windows, onChange }: { windows: BlackoutWindow[]; onChange: (w: BlackoutWindow[]) => void }) {
    const update = (i: number, w: BlackoutWindow) => onChange(windows.map((x, j) => (j === i ? w : x)));
    const toggleDay = (i: number, day: string) => {
        const days = windows[i].days?.length ? windows[i].days! : [...DAYS];
        const next = days.includes(day) ? days.filter(d => d !== day) : [...days, day];
        update(i, { ...windows[i], days: next.length === 7 ? [] : DAYS.filter(d => next.includes(d)) });
    };
    return (
        <div className="space-y-2">
            {windows.map((w, i) => {
                const on = (d: string) => !w.days?.length || w.days.includes(d);
                const error = windowError(w);
                const night = nightText(w);
                return (
                    <div key={i} className="rounded-lg border border-gray-700 bg-gray-900/40 p-3 space-y-2">
                        <div className="flex flex-wrap items-center gap-3">
                            <div className="flex gap-1" role="group" aria-label={t("Days")}>
                                {DAYS.map(d => (
                                    <button key={d} type="button" onClick={() => toggleDay(i, d)} aria-pressed={on(d)} title={t(DAY_SHORT[d])}
                                        className={`w-7 h-7 rounded-md text-xs font-medium ${on(d) ? "bg-blue-600 text-white" : "bg-gray-800 text-gray-500 hover:text-gray-200"}`}>
                                        {letter(d)}
                                    </button>
                                ))}
                            </div>
                            <label className="flex items-center gap-1.5 text-sm text-gray-300 cursor-pointer">
                                <input type="checkbox" className="accent-blue-500" checked={allDay(w)}
                                    onChange={e => update(i, e.target.checked ? { ...w, from: "00:00", to: "24:00" } : { ...w, from: "22:00", to: "06:00" })} />
                                {t("All day")}
                            </label>
                            {!allDay(w) && (
                                <span className="flex items-center gap-1.5 text-sm text-gray-300">
                                    {t("from")}
                                    <input type="time" value={w.from} onChange={e => update(i, { ...w, from: e.target.value })} aria-label={t("From")}
                                        className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-gray-100" />
                                    {t("to")}
                                    <input type="time" value={w.to === "24:00" ? "00:00" : w.to} onChange={e => update(i, { ...w, to: e.target.value })} aria-label={t("To")}
                                        className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-gray-100" />
                                </span>
                            )}
                            <button type="button" onClick={() => onChange(windows.filter((_, j) => j !== i))} className="ml-auto text-gray-500 hover:text-red-300" aria-label={t("Remove")} title={t("Remove")}>
                                <X size={16} />
                            </button>
                        </div>
                        {error ? <p className="text-xs text-red-300">{error}</p> : night && <p className="text-xs text-gray-400">{night}</p>}
                    </div>
                );
            })}
            <div className="flex flex-wrap gap-2">
                <Button type="button" variant="secondary" size="sm" className="gap-1.5" onClick={() => onChange([...windows, { days: ["mon", "tue", "wed", "thu", "fri"], from: "22:00", to: "06:00" }])}>
                    <Plus size={14} /> {t("Add hours")}
                </Button>
                {PRESETS.map(p => (
                    <Button key={p.label} type="button" variant="ghost" size="sm" className="gap-1.5 text-gray-300" onClick={() => onChange([...windows, ...p.windows])}>
                        <Plus size={14} /> {t(p.label)}
                    </Button>
                ))}
            </div>
        </div>
    );
}


const SEGMENT_STYLE = {
    blocked: "rgba(100,116,139,.85)", // slate: can't be woken
    allow: "rgba(245,158,11,.75)", // amber: exception, can be woken
    on: "rgba(34,197,94,.8)", // green: exception, kept on
};

// BlockedWeek previews the 7 days ahead: when the application can't be woken, and exceptions.
export function BlockedWeek({ view, now }: { view: BlockedHoursView; now: number }) {
    return (
        <div className="space-y-1.5">
            {view.week.map((day, i) => {
                const [y, m, d] = day.date.split("-").map(Number);
                // The day as the server counts it (its time zone): from its first segment to its last.
                const start = new Date(day.segments[0]?.from ?? new Date(y, m - 1, d)).getTime();
                const end = new Date(day.segments[day.segments.length - 1]?.to ?? new Date(y, m - 1, d + 1)).getTime();
                const pos = (iso: string) => ((new Date(iso).getTime() - start) / (end - start)) * 100;
                const label = new Date(y, m - 1, d).toLocaleDateString(getLang(), { weekday: "short", day: "numeric" });
                return (
                    <div key={day.date} className="flex items-center gap-3">
                        <div className="w-24 shrink-0 text-xs text-gray-400 truncate" title={day.holiday && day.holiday !== "-" ? day.holiday : undefined}>
                            {i === 0 ? t("Today") : label}
                            {day.holiday && <span className="ml-1 text-[10px] text-amber-300">{t("holiday")}</span>}
                        </div>
                        <div className="relative flex-1 h-4 rounded-sm bg-gray-700/40 overflow-hidden">
                            {day.segments.filter(s => s.blocked || s.exception).map(s => (
                                <div key={s.from} className="absolute top-0 bottom-0"
                                    title={`${new Date(s.from).toLocaleTimeString(getLang(), { hour: "2-digit", minute: "2-digit", hour12: false })}–${new Date(s.to).toLocaleTimeString(getLang(), { hour: "2-digit", minute: "2-digit", hour12: false })}`}
                                    style={{ left: `${pos(s.from)}%`, width: `${pos(s.to) - pos(s.from)}%`, background: s.exception ? SEGMENT_STYLE[s.exception] : SEGMENT_STYLE.blocked }} />
                            ))}
                            {i === 0 && <div className="absolute top-0 bottom-0 w-0.5 bg-white/80" style={{ left: `${((now - start) / (end - start)) * 100}%` }} title={t("Now")} />}
                        </div>
                    </div>
                );
            })}
            <div className="flex items-center gap-3">
                <div className="w-24 shrink-0" />
                <div className="flex-1 flex justify-between text-[10px] text-gray-500 tabular-nums">
                    {["00", "06", "12", "18", "24"].map(h => <span key={h}>{h}</span>)}
                </div>
            </div>
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-400 pt-1">
                <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm" style={{ background: SEGMENT_STYLE.blocked }} /> {t("Can't be woken")}</span>
                <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm bg-gray-700/60" /> {t("Woken by requests")}</span>
                <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm" style={{ background: SEGMENT_STYLE.allow }} /> {t("Exception: can be woken")}</span>
                <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-sm" style={{ background: SEGMENT_STYLE.on }} /> {t("Exception: kept on")}</span>
                <span className="text-gray-500">({view.timezone})</span>
            </div>
        </div>
    );
}

// WhenBlockedEditor says what browsers get during blocked hours: 503, or a page offering to start
// the application anyway, with an optional message.
export function WhenBlockedEditor({ policy, onChange }: { policy: BlackoutPolicy; onChange: (p: BlackoutPolicy) => void }) {
    const ask = policy.when_blocked === "ask";
    return (
        <div className="space-y-2">
            <div className="text-xs text-gray-400">{t("During blocked hours, someone opening it in a browser…")}</div>
            <div className="grid sm:grid-cols-2 gap-2">
                {([
                    ["refuse", t("Gets an error (503)"), t("Nobody can start it until the blocked hours end, except from the dashboard.")],
                    ["ask", t("Can start it from a page"), t("A page says it's off outside its hours, with a Start it now button. API calls and monitors still get 503.")],
                ] as ["refuse" | "ask", string, string][]).map(([value, label, hint]) => (
                    <label key={value} className={`flex items-start gap-2 px-3 py-2 rounded-lg border cursor-pointer ${(policy.when_blocked || "refuse") === value ? "border-blue-500 bg-blue-500/10" : "border-gray-700 hover:border-gray-600"}`}>
                        <input type="radio" className="mt-1 accent-blue-500" checked={(policy.when_blocked || "refuse") === value} onChange={() => onChange({ ...policy, when_blocked: value })} />
                        <span>
                            <span className="block text-sm text-gray-100">{label}</span>
                            <span className="block text-xs text-gray-400">{hint}</span>
                        </span>
                    </label>
                ))}
            </div>
            {ask && (
                <label className="block space-y-1">
                    <span className="text-xs text-gray-400">{t("Message on the page (optional)")}</span>
                    <textarea value={policy.message || ""} onChange={e => onChange({ ...policy, message: e.target.value })} maxLength={300} rows={2}
                        placeholder={t("e.g. Urgent? Write to team-x@example.com")}
                        className="w-full bg-gray-900 border border-gray-700 rounded-md px-3 py-2 text-sm text-gray-100" />
                </label>
            )}
        </div>
    );
}
