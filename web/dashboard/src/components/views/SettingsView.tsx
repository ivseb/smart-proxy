import { useState } from "react";
import { CalendarHeart, Globe2, Layers, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { WindowsEditor } from "@/components/views/BlockedParts";
import { usePolling } from "@/hooks/usePolling";
import { apiRequest, errorMessage } from "@/lib/api";
import { t, tn } from "@/lib/i18n";
import { holidayText, italianHolidays, policyText, sortHolidays, windowsValid } from "@/lib/blackout";
import type { BlackoutPolicy, ClusterInfo, Holiday, Settings } from "@/types/api";

const DEFAULT_WINDOWS = [
    { days: ["mon", "tue", "wed", "thu", "fri"], from: "22:00", to: "06:00" },
    { days: ["sat", "sun"], from: "00:00", to: "24:00" },
];

const ZONES = ["Europe/Rome", "Europe/London", "Europe/Berlin", "Europe/Madrid", "Europe/Paris", "UTC", "America/New_York"];

// SettingsView edits the installation-wide settings: blocked hours (global and per namespace),
// holidays, time zone. One form, saved at once.
export function SettingsView({ info }: { info: ClusterInfo | null }) {
    const { data, refetch } = usePolling<Settings>("/api/settings", 60_000);
    const [draft, setDraft] = useState<Settings | null>(null);
    const [busy, setBusy] = useState(false);
    if (!data) return <div className="text-gray-500 text-sm py-12 text-center">{t("Loading…")}</div>;
    const s = draft ?? data;
    const change = (next: Settings) => setDraft(next);
    const dirty = draft !== null;

    const namespaces = [...new Set([...(info?.namespaces || []), ...Object.keys(s.namespaces)])].sort();
    const valid = (p: BlackoutPolicy) => p.mode !== "custom" || windowsValid(p.windows || []);
    const allValid = valid(s.global) && Object.values(s.namespaces).every(valid) && !!s.timezone;

    const save = async () => {
        setBusy(true);
        try {
            await apiRequest("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(s) });
            toast.success(t("Settings saved"));
            setDraft(null);
            refetch();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    const setNamespace = (ns: string, p: BlackoutPolicy | null) => {
        const next = { ...s.namespaces };
        if (p) next[ns] = p;
        else delete next[ns];
        change({ ...s, namespaces: next });
    };

    return (
        <div className="space-y-6 pb-20">
            <Card>
                <CardHeader>
                    <CardTitle className="flex items-center gap-2"><Globe2 size={18} /> {t("Blocked hours")}</CardTitle>
                    <p className="text-gray-400 text-sm max-w-3xl">
                        {t("During blocked hours requests don't start sleeping applications: they get 503. When blocked hours start, applications are put to sleep. These are the defaults: a namespace or a route can have its own, and exceptions lift them for a while (on each route's page). Always On routes are never blocked.")}
                    </p>
                </CardHeader>
                <CardContent className="space-y-3">
                    <label className="flex items-center gap-2 text-sm text-gray-200 cursor-pointer">
                        <input type="checkbox" className="accent-blue-500" checked={s.global.mode === "custom"}
                            onChange={e => change({ ...s, global: e.target.checked ? { mode: "custom", windows: s.global.windows?.length ? s.global.windows : DEFAULT_WINDOWS } : { mode: "off", windows: s.global.windows } })} />
                        {t("Block every route at these hours, unless its namespace or the route says otherwise")}
                    </label>
                    {s.global.mode === "custom" && <WindowsEditor windows={s.global.windows || []} onChange={windows => change({ ...s, global: { mode: "custom", windows } })} />}
                </CardContent>
            </Card>

            <Card>
                <CardHeader>
                    <CardTitle className="flex items-center gap-2"><Layers size={18} /> {t("By namespace")}</CardTitle>
                    <p className="text-gray-400 text-sm">{t("A namespace can follow the global hours, have its own, or none.")}</p>
                </CardHeader>
                <CardContent className="p-0">
                    {namespaces.length === 0 ? <p className="p-6 text-sm text-gray-500">{t("No namespaces")}</p> : (
                        <ul className="divide-y divide-gray-700">
                            {namespaces.map(ns => {
                                const p = s.namespaces[ns];
                                const choice = !p?.mode ? "inherit" : p.mode;
                                return (
                                    <li key={ns} className="px-6 py-3 space-y-2">
                                        <div className="flex flex-wrap items-center gap-3">
                                            <span className="font-mono text-sm text-gray-100 min-w-[10rem]">{ns}</span>
                                            <select value={choice} aria-label={t("Blocked hours of {ns}", { ns })}
                                                onChange={e => setNamespace(ns, e.target.value === "inherit" ? null : e.target.value === "off" ? { mode: "off" } : { mode: "custom", windows: s.global.windows?.length ? s.global.windows : DEFAULT_WINDOWS })}
                                                className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-sm text-gray-100">
                                                <option value="inherit">{t("As the global settings")}</option>
                                                <option value="custom">{t("Its own hours")}</option>
                                                <option value="off">{t("No blocked hours")}</option>
                                            </select>
                                            <span className="text-xs text-gray-400">{policyText(choice === "inherit" ? s.global : p)}</span>
                                        </div>
                                        {p?.mode === "custom" && <WindowsEditor windows={p.windows || []} onChange={windows => setNamespace(ns, { mode: "custom", windows })} />}
                                    </li>
                                );
                            })}
                        </ul>
                    )}
                </CardContent>
            </Card>

            <HolidaysCard holidays={s.holidays} onChange={holidays => change({ ...s, holidays })} />

            <Card>
                <CardHeader><CardTitle>{t("Time zone")}</CardTitle></CardHeader>
                <CardContent className="space-y-2">
                    <input list="zones" value={s.timezone} onChange={e => change({ ...s, timezone: e.target.value })} aria-label={t("Time zone")}
                        className="bg-gray-900 border border-gray-700 rounded-md px-3 py-1.5 text-sm text-gray-100 w-64" />
                    <datalist id="zones">{ZONES.map(z => <option key={z} value={z} />)}</datalist>
                    <p className="text-xs text-gray-500">{t("Blocked hours and holidays are in this time zone (IANA name, e.g. Europe/Rome).")}</p>
                </CardContent>
            </Card>

            {dirty && (
                <div className="fixed bottom-0 inset-x-0 z-40 border-t border-gray-700 bg-gray-900/95 backdrop-blur">
                    <div className="mx-auto max-w-7xl px-4 sm:px-6 py-3 flex items-center justify-end gap-2">
                        <span className="mr-auto text-sm text-gray-300">{t("Unsaved changes")}</span>
                        <Button variant="ghost" onClick={() => setDraft(null)}>{t("Discard")}</Button>
                        <Button onClick={save} disabled={busy || !allValid}>{t("Save")}</Button>
                    </div>
                </div>
            )}
        </div>
    );
}

function HolidaysCard({ holidays, onChange }: { holidays: Holiday[]; onChange: (h: Holiday[]) => void }) {
    const [date, setDate] = useState("");
    const [yearly, setYearly] = useState(true);
    const [name, setName] = useState("");
    const sorted = sortHolidays(holidays);
    const add = (list: Holiday[]) => {
        const have = new Set(holidays.map(h => h.date));
        const added = list.filter(h => !have.has(h.date));
        onChange([...holidays, ...added]);
        return added.length;
    };
    const addOne = () => {
        if (!date) return;
        add([{ date: yearly ? date.slice(5) : date, name: name.trim() || undefined }]);
        setDate("");
        setName("");
    };
    const year = new Date().getFullYear();
    return (
        <Card>
            <CardHeader className="flex flex-wrap items-start justify-between gap-3">
                <div>
                    <CardTitle className="flex items-center gap-2"><CalendarHeart size={18} /> {t("Holidays")}</CardTitle>
                    <p className="text-gray-400 text-sm">{t("On holidays, blocked hours follow Sunday's rules.")}</p>
                </div>
                <Button variant="secondary" size="sm" onClick={() => {
                    const n = add(italianHolidays([year, year + 1]));
                    toast.success(n ? tn(n, "{n} holiday added", "{n} holidays added") : t("Already all there"));
                }}>{t("Add Italy's holidays")}</Button>
            </CardHeader>
            <CardContent className="space-y-3">
                {sorted.length === 0 ? <p className="text-sm text-gray-500">{t("No holidays yet.")}</p> : (
                    <ul className="grid sm:grid-cols-2 lg:grid-cols-3 gap-1.5">
                        {sorted.map(h => (
                            <li key={h.date} className="flex items-center gap-2 rounded-md border border-gray-700 px-3 py-1.5 text-sm">
                                <span className="text-gray-100">{h.name || "—"}</span>
                                <span className="text-xs text-gray-400 truncate">{holidayText(h)}</span>
                                <button onClick={() => onChange(holidays.filter(x => x.date !== h.date))} className="ml-auto text-gray-500 hover:text-red-300" aria-label={t("Remove")} title={t("Remove")}>
                                    <Trash2 size={14} />
                                </button>
                            </li>
                        ))}
                    </ul>
                )}
                <div className="flex flex-wrap items-center gap-2 pt-1">
                    <input type="date" value={date} onChange={e => setDate(e.target.value)} aria-label={t("Date")} className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-sm text-gray-100" />
                    <label className="flex items-center gap-1.5 text-sm text-gray-300 cursor-pointer">
                        <input type="checkbox" className="accent-blue-500" checked={yearly} onChange={e => setYearly(e.target.checked)} /> {t("every year")}
                    </label>
                    <input value={name} onChange={e => setName(e.target.value)} placeholder={t("Name (optional)")} className="bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-sm text-gray-100 w-48" />
                    <Button variant="secondary" size="sm" className="gap-1.5" onClick={addOne} disabled={!date}><Plus size={14} /> {t("Add")}</Button>
                </div>
            </CardContent>
        </Card>
    );
}
