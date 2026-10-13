import { useEffect, useState } from "react";
import { Pause as PauseIcon, Play, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/Button";
import { useNow } from "@/hooks/useNow";
import { apiRequest, errorMessage } from "@/lib/api";
import { formatDuration, workloadLabel } from "@/lib/format";
import { t } from "@/lib/i18n";
import { pauseActive, pausedUntil, pauseText, tomorrowMorning } from "@/lib/pause";
import type { RouteStatus } from "@/types/api";

type Choice = "1h" | "4h" | "morning" | "custom" | "forever";

const pad = (n: number) => String(n).padStart(2, "0");
// The value of a datetime-local input for a date (local time, minutes).
const localInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;

// PauseButton pauses a route (with a dialog to choose for how long) or resumes it.
export function PauseButton({ route, onChanged, compact }: { route: RouteStatus; onChanged: () => void; compact?: boolean }) {
    const now = useNow(30000);
    const [open, setOpen] = useState(false);
    const paused = pauseActive(route.pause, now);

    const resume = async () => {
        try {
            await apiRequest(`/api/routes/pause?${new URLSearchParams({ id: route.id })}`, { method: "DELETE" });
            toast.success(t("Route resumed: it sleeps again after {duration} without traffic", { duration: formatDuration(route.effective_idle_timeout) }));
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            onChanged();
        }
    };

    const button = paused ? (
        compact ? (
            <Button variant="ghost" size="icon" onClick={resume} title={t("Resume")} aria-label={t("Resume")}>
                <Play className="w-4 h-4 text-amber-300" />
            </Button>
        ) : (
            <Button variant="secondary" onClick={resume} className="gap-2">
                <Play className="w-4 h-4 text-amber-300" /> {t("Resume")}
            </Button>
        )
    ) : compact ? (
        <Button variant="ghost" size="icon" onClick={() => setOpen(true)} title={t("Pause")} aria-label={t("Pause")}>
            <PauseIcon className="w-4 h-4 text-gray-300" />
        </Button>
    ) : (
        <Button variant="secondary" onClick={() => setOpen(true)} className="gap-2">
            <PauseIcon className="w-4 h-4 text-amber-300" /> {t("Pause")}
        </Button>
    );

    return (
        <>
            {button}
            {open && <PauseDialog route={route} onClose={() => setOpen(false)} onDone={() => { setOpen(false); onChanged(); }} />}
        </>
    );
}

function PauseDialog({ route, onClose, onDone }: { route: RouteStatus; onClose: () => void; onDone: () => void }) {
    const [now] = useState(() => Date.now());
    const [choice, setChoice] = useState<Choice>("1h");
    const [custom, setCustom] = useState(() => localInput(new Date(now + 2 * 3600_000)));
    const [busy, setBusy] = useState(false);

    useEffect(() => {
        const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);

    const until = (): Date | null => {
        switch (choice) {
            case "1h": return new Date(Date.now() + 3600_000);
            case "4h": return new Date(Date.now() + 4 * 3600_000);
            case "morning": return tomorrowMorning(Date.now());
            case "custom": return custom ? new Date(custom) : null;
            default: return null;
        }
    };
    const customInvalid = choice === "custom" && (!custom || new Date(custom).getTime() <= now);

    const submit = async () => {
        const end = until();
        setBusy(true);
        try {
            const params = new URLSearchParams({ id: route.id });
            if (end) params.set("until", end.toISOString());
            await apiRequest(`/api/routes/pause?${params}`, { method: "POST" });
            toast.success(end ? pausedUntil(end.toISOString(), Date.now()) : t("Paused until resumed"));
            onDone();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    const options: { value: Choice; label: string }[] = [
        { value: "1h", label: t("1 hour") },
        { value: "4h", label: t("4 hours") },
        { value: "morning", label: t("Until tomorrow at 08:00") },
        { value: "custom", label: t("Until…") },
        { value: "forever", label: t("Until I resume it") },
    ];

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm" role="dialog" aria-modal="true" onClick={e => e.stopPropagation()}>
            <div className="bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full max-w-md">
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-center gap-3">
                    <h2 className="text-lg font-bold text-white truncate">{t("Pause {name}", { name: route.host || workloadLabel(route.deployment) })}</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label={t("Close")}><X className="w-6 h-6" /></button>
                </div>
                <div className="p-6 space-y-4">
                    <p className="text-sm text-gray-300">
                        {t("While paused, Smart Proxy only forwards requests to {workload}: it is woken now and isn't put to sleep. Other backends, the waking page and the answers to monitors are off. Sign-in, if required, stays on.", { workload: workloadLabel(route.deployment) })}
                    </p>
                    <fieldset className="space-y-1.5">
                        <legend className="text-xs text-gray-400 mb-1">{t("For how long")}</legend>
                        {options.map(o => (
                            <label key={o.value} className={`flex items-center gap-3 px-3 py-2 rounded-lg border cursor-pointer ${choice === o.value ? "border-blue-500 bg-blue-500/10" : "border-gray-700 hover:border-gray-600"}`}>
                                <input type="radio" name="pause" value={o.value} checked={choice === o.value} onChange={() => setChoice(o.value)} className="accent-blue-500" />
                                <span className="text-sm text-gray-100">{o.label}</span>
                                {o.value === "custom" && choice === "custom" && (
                                    <input type="datetime-local" value={custom} min={localInput(new Date())} onChange={e => setCustom(e.target.value)}
                                        className="ml-auto bg-gray-900 border border-gray-700 rounded-md px-2 py-1 text-sm text-gray-100" aria-label={t("Until")} />
                                )}
                            </label>
                        ))}
                    </fieldset>
                    <p className="text-xs text-gray-500">
                        {choice === "forever"
                            ? t("It stays paused until you resume it, here or from the list of routes.")
                            : t("Then it goes back to normal by itself, and sleeps after {duration} without traffic.", { duration: formatDuration(route.effective_idle_timeout) })}
                    </p>
                </div>
                <div className="px-6 py-4 border-t border-gray-700 flex justify-end gap-2">
                    <Button variant="ghost" onClick={onClose}>{t("Cancel")}</Button>
                    <Button onClick={submit} disabled={busy || customInvalid} className="gap-2"><PauseIcon className="w-4 h-4" /> {t("Pause")}</Button>
                </div>
            </div>
        </div>
    );
}

// PauseBadge says that a route is paused, and until when.
export function PauseBadge({ route, now }: { route: RouteStatus; now: number }) {
    if (!pauseActive(route.pause, now)) return null;
    const by = route.pause.by ? ` · ${t("by {user}", { user: route.pause.by })}` : "";
    return (
        <span className="inline-flex items-center gap-1 text-amber-300" title={`${pauseText(route.pause, now)}${by}`}>
            <PauseIcon size={12} /> {pauseText(route.pause, now)}
        </span>
    );
}
