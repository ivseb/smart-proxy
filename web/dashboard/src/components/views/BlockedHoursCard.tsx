import { useEffect, useState } from "react";
import { Ban, CalendarPlus, Power, ShieldCheck, X } from "lucide-react";
import { toast } from "sonner";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { BlockedWeek, WindowsEditor } from "@/components/views/BlockedParts";
import { useNow } from "@/hooks/useNow";
import { usePolling } from "@/hooks/usePolling";
import { apiRequest, errorMessage } from "@/lib/api";
import { getLang, t } from "@/lib/i18n";
import { policyText, stateText, whenText, windowsValid } from "@/lib/blackout";
import { workloadLabel } from "@/lib/format";
import type { BlackoutPolicy, BlackoutWindow, BlockedHoursView, RouteConfig, RouteException, RouteStatus, Settings } from "@/types/api";

type Choice = "inherit" | "custom" | "off";

// BlockedHoursCard shows and edits when a route's application may not be woken, and its exceptions.
export function BlockedHoursCard({ route, onSave, onChanged }: { route: RouteStatus; onSave: (r: Partial<RouteConfig>) => Promise<boolean>; onChanged: () => void }) {
    const now = useNow(30_000);
    const { data: view, refetch } = usePolling<BlockedHoursView>(`/api/routes/blocked-hours?${new URLSearchParams({ id: route.id })}`, 30_000);
    const { data: settings } = usePolling<Settings>("/api/settings", 60_000);
    const [editing, setEditing] = useState<{ choice: Choice; windows: BlackoutWindow[] } | null>(null);
    const [adding, setAdding] = useState(false);

    const own = route.blackout?.mode ? route.blackout : null;
    const current: Choice = !own ? "inherit" : own.mode === "off" ? "off" : "custom";
    const inheritedFrom = settings?.namespaces[route.namespace]?.mode ? "namespace" : "global";
    const inherited: BlackoutPolicy | undefined = settings ? (settings.namespaces[route.namespace]?.mode ? settings.namespaces[route.namespace] : settings.global) : undefined;
    const state = route.blocked_hours;
    const status = stateText(state, now);

    const startEditing = () => setEditing({ choice: current, windows: own?.windows?.length ? own.windows : inherited?.windows?.length ? inherited.windows : [{ days: ["mon", "tue", "wed", "thu", "fri"], from: "22:00", to: "06:00" }] });
    const save = async () => {
        if (!editing) return;
        const blackout: BlackoutPolicy | null = editing.choice === "inherit" ? null : editing.choice === "off" ? { mode: "off" } : { mode: "custom", windows: editing.windows };
        if (await onSave({ ...route, blackout })) {
            setEditing(null);
            refetch();
        }
    };

    const end = async (e: RouteException) => {
        try {
            await apiRequest(`/api/routes/exceptions?${new URLSearchParams({ id: route.id, exception: e.id })}`, { method: "DELETE" });
            toast.success(new Date(e.from).getTime() <= now ? t("Exception ended") : t("Exception removed"));
        } catch (err) {
            toast.error(t("Failed: {error}", { error: errorMessage(err) }));
        } finally {
            onChanged();
            refetch();
        }
    };
    const exceptions = (route.exceptions || []).filter(e => new Date(e.to).getTime() > now);

    return (
        <Card>
            <CardHeader className="flex flex-wrap items-start justify-between gap-3">
                <div className="space-y-1">
                    <CardTitle>{t("Blocked hours")}</CardTitle>
                    <p className="text-gray-400 text-sm max-w-2xl">{t("During blocked hours requests don't start the application: they get 503. When blocked hours start, the application is put to sleep.")}</p>
                </div>
                {status && (
                    <span className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-sm ${state.blocked ? "bg-slate-600/40 text-slate-200" : state.exception ? "bg-amber-500/15 text-amber-200" : "bg-gray-700/50 text-gray-300"}`}>
                        {state.blocked ? <Ban size={14} /> : state.exception?.kind === "on" ? <Power size={14} /> : <ShieldCheck size={14} />} {status}
                    </span>
                )}
            </CardHeader>
            <CardContent className="space-y-6">
                {/* Rules */}
                <section className="space-y-3">
                    <div className="flex flex-wrap items-baseline justify-between gap-2">
                        <h4 className="text-sm font-medium text-gray-200">{t("Rules")}</h4>
                        {!editing && <Button variant="ghost" size="sm" onClick={startEditing}>{t("Change")}</Button>}
                    </div>
                    {!editing ? (
                        <p className="text-sm text-gray-300">
                            {current === "inherit" && <span className="text-gray-500">{inheritedFrom === "namespace" ? t("As the namespace {ns}:", { ns: route.namespace }) : t("As the global settings:")} </span>}
                            {current === "custom" && <span className="text-gray-500">{t("Its own:")} </span>}
                            {policyText(current === "inherit" ? inherited : own)}
                            {state.holiday && <span className="ml-2 text-amber-300 text-xs">{t("Today is a holiday: Sunday's rules apply")}</span>}
                        </p>
                    ) : (
                        <div className="space-y-3">
                            <fieldset className="space-y-1.5">
                                {([
                                    ["inherit", inheritedFrom === "namespace" ? t("As the namespace {ns}", { ns: route.namespace }) : t("As the global settings"), policyText(inherited)],
                                    ["custom", t("Its own hours"), t("Only for this route")],
                                    ["off", t("No blocked hours"), t("Requests can always start it")],
                                ] as [Choice, string, string][]).map(([value, label, hint]) => (
                                    <label key={value} className={`flex items-start gap-3 px-3 py-2 rounded-lg border cursor-pointer ${editing.choice === value ? "border-blue-500 bg-blue-500/10" : "border-gray-700 hover:border-gray-600"}`}>
                                        <input type="radio" name={`blocked-${route.id}`} className="mt-1 accent-blue-500" checked={editing.choice === value} onChange={() => setEditing({ ...editing, choice: value })} />
                                        <span>
                                            <span className="block text-sm text-gray-100">{label}</span>
                                            <span className="block text-xs text-gray-400">{hint}</span>
                                        </span>
                                    </label>
                                ))}
                            </fieldset>
                            {editing.choice === "custom" && <WindowsEditor windows={editing.windows} onChange={windows => setEditing({ ...editing, windows })} />}
                            <div className="flex justify-end gap-2">
                                <Button variant="ghost" onClick={() => setEditing(null)}>{t("Cancel")}</Button>
                                <Button onClick={save} disabled={editing.choice === "custom" && !windowsValid(editing.windows)}>{t("Save")}</Button>
                            </div>
                        </div>
                    )}
                </section>

                {/* Week */}
                {view && <BlockedWeek view={view} now={now} />}

                {/* Exceptions */}
                <section className="space-y-2">
                    <div className="flex flex-wrap items-baseline justify-between gap-2">
                        <h4 className="text-sm font-medium text-gray-200">{t("Exceptions")}</h4>
                        <Button variant="secondary" size="sm" className="gap-1.5" onClick={() => setAdding(true)}><CalendarPlus size={14} /> {t("Add an exception")}</Button>
                    </div>
                    {exceptions.length === 0 ? (
                        <p className="text-sm text-gray-500">{t("None. An exception lets the application be started (or keeps it on) for a while, blocked hours or not: for a release on Saturday, a demo in the evening…")}</p>
                    ) : (
                        <ul className="space-y-1.5">
                            {exceptions.map(e => {
                                const active = new Date(e.from).getTime() <= now;
                                return (
                                    <li key={e.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-gray-700 px-3 py-2 text-sm">
                                        <span className={`inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded ${e.kind === "on" ? "bg-green-500/15 text-green-300" : "bg-amber-500/15 text-amber-200"}`}>
                                            {e.kind === "on" ? <Power size={12} /> : <ShieldCheck size={12} />} {e.kind === "on" ? t("Kept on") : t("Can be woken")}
                                        </span>
                                        <span className="text-gray-200 tabular-nums">{periodText(e, now)}</span>
                                        {active && <span className="text-xs text-green-300">{t("in force")}</span>}
                                        {e.reason && <span className="text-gray-400 truncate max-w-xs" title={e.reason}>{e.reason}</span>}
                                        {e.by && <span className="text-xs text-gray-500">{t("by {user}", { user: e.by })}</span>}
                                        <Button variant="ghost" size="sm" className="ml-auto text-gray-400 hover:text-red-300" onClick={() => end(e)}>
                                            {active ? t("End now") : t("Remove")}
                                        </Button>
                                    </li>
                                );
                            })}
                        </ul>
                    )}
                </section>
            </CardContent>
            {adding && <ExceptionDialog route={route} onClose={() => setAdding(false)} onDone={() => { setAdding(false); onChanged(); refetch(); }} />}
        </Card>
    );
}

function periodText(e: RouteException, now: number): string {
    return `${whenText(e.from, now)} → ${whenText(e.to, now)}`;
}

const pad = (n: number) => String(n).padStart(2, "0");
const localInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
const at = (base: Date, days: number, hours = 0) => new Date(base.getFullYear(), base.getMonth(), base.getDate() + days, hours);

// Quick choices for an exception's time, from now.
function presets(now: Date): { label: string; from: Date; to: Date }[] {
    const toSat = (6 - now.getDay() + 7) % 7 || 7; // Next Saturday (a week ahead on Saturdays)
    const list = [
        { label: t("Now, for 2 hours"), from: now, to: new Date(now.getTime() + 2 * 3600_000) },
        { label: t("Tonight, until 08:00"), from: now, to: at(now, now.getHours() < 8 ? 0 : 1, 8) },
        { label: t("Tomorrow, all day"), from: at(now, 1), to: at(now, 2) },
        { label: t("Saturday {date}", { date: at(now, toSat).toLocaleDateString(getLang(), { day: "numeric", month: "short" }) }), from: at(now, toSat), to: at(now, toSat + 1) },
        { label: t("The weekend of {date}", { date: at(now, toSat).toLocaleDateString(getLang(), { day: "numeric", month: "short" }) }), from: at(now, toSat), to: at(now, toSat + 2) },
    ];
    return list;
}

function ExceptionDialog({ route, onClose, onDone }: { route: RouteStatus; onClose: () => void; onDone: () => void }) {
    const [start] = useState(() => new Date());
    const choices = presets(start);
    const [kind, setKind] = useState<"allow" | "on">("allow");
    const [from, setFrom] = useState(localInput(choices[3].from));
    const [to, setTo] = useState(localInput(choices[3].to));
    const [reason, setReason] = useState("");
    const [busy, setBusy] = useState(false);

    useEffect(() => {
        const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);

    const fromDate = new Date(from), toDate = new Date(to);
    const invalid = !from || !to || toDate <= fromDate || toDate.getTime() <= start.getTime();
    const tooLong = toDate.getTime() - fromDate.getTime() > 31 * 86_400_000;
    const name = route.host || workloadLabel(route.deployment);

    const submit = async () => {
        setBusy(true);
        try {
            await apiRequest(`/api/routes/exceptions?${new URLSearchParams({ id: route.id })}`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ kind, from: fromDate.toISOString(), to: toDate.toISOString(), reason: reason.trim() }),
            });
            toast.success(t("Exception added"));
            onDone();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm" role="dialog" aria-modal="true">
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-lg">
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-center gap-3">
                    <h2 className="text-lg font-bold text-white truncate">{t("Exception for {name}", { name })}</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label={t("Close")}><X className="w-6 h-6" /></button>
                </div>
                <div className="p-6 space-y-5">
                    <fieldset className="grid sm:grid-cols-2 gap-2">
                        {([
                            ["allow", t("Can be woken"), t("Requests start it as usual; it sleeps when idle.")],
                            ["on", t("Kept on"), t("Started when the exception begins and kept on until it ends, even without traffic.")],
                        ] as ["allow" | "on", string, string][]).map(([value, label, hint]) => (
                            <label key={value} className={`flex items-start gap-2 px-3 py-2 rounded-lg border cursor-pointer ${kind === value ? "border-blue-500 bg-blue-500/10" : "border-gray-700 hover:border-gray-600"}`}>
                                <input type="radio" name="kind" className="mt-1 accent-blue-500" checked={kind === value} onChange={() => setKind(value)} />
                                <span>
                                    <span className="block text-sm text-gray-100">{label}</span>
                                    <span className="block text-xs text-gray-400">{hint}</span>
                                </span>
                            </label>
                        ))}
                    </fieldset>
                    <div className="space-y-2">
                        <div className="text-xs text-gray-400">{t("When")}</div>
                        <div className="flex flex-wrap gap-1.5">
                            {choices.map(c => {
                                const selected = from === localInput(c.from) && to === localInput(c.to);
                                return (
                                    <button key={c.label} type="button" onClick={() => { setFrom(localInput(c.from)); setTo(localInput(c.to)); }}
                                        className={`px-2.5 py-1 rounded-full text-xs border ${selected ? "border-blue-500 bg-blue-500/15 text-blue-100" : "border-gray-700 text-gray-300 hover:border-gray-500"}`}>
                                        {c.label}
                                    </button>
                                );
                            })}
                        </div>
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-sm">
                            <label className="space-y-1">
                                <span className="block text-xs text-gray-500">{t("From")}</span>
                                <input type="datetime-local" value={from} onChange={e => setFrom(e.target.value)} className="w-full bg-gray-900 border border-gray-700 rounded-md px-2 py-1.5 text-gray-100" />
                            </label>
                            <label className="space-y-1">
                                <span className="block text-xs text-gray-500">{t("To")}</span>
                                <input type="datetime-local" value={to} onChange={e => setTo(e.target.value)} className="w-full bg-gray-900 border border-gray-700 rounded-md px-2 py-1.5 text-gray-100" />
                            </label>
                        </div>
                        {tooLong && <p className="text-xs text-red-300">{t("An exception lasts 31 days at most.")}</p>}
                    </div>
                    <label className="block space-y-1">
                        <span className="text-xs text-gray-400">{t("Why (optional)")}</span>
                        <input value={reason} onChange={e => setReason(e.target.value)} maxLength={200} placeholder={t("e.g. release on Saturday")}
                            className="w-full bg-gray-900 border border-gray-700 rounded-md px-3 py-2 text-sm text-gray-100" />
                    </label>
                    {!invalid && !tooLong && (
                        <p className="text-sm text-gray-300 bg-gray-900/60 rounded-lg px-3 py-2">
                            {kind === "on"
                                ? t("{name} will be on from {from} to {to}.", { name, from: whenText(fromDate.toISOString(), start.getTime()), to: whenText(toDate.toISOString(), start.getTime()) })
                                : t("From {from} to {to}, requests can start {name}.", { name, from: whenText(fromDate.toISOString(), start.getTime()), to: whenText(toDate.toISOString(), start.getTime()) })}
                        </p>
                    )}
                </div>
                <div className="px-6 py-4 border-t border-gray-700 flex justify-end gap-2">
                    <Button variant="ghost" onClick={onClose}>{t("Cancel")}</Button>
                    <Button onClick={submit} disabled={busy || invalid || tooLong}>{t("Add the exception")}</Button>
                </div>
            </div>
        </div>
    );
}
