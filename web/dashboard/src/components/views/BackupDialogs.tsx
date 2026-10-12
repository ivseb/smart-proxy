import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, ArchiveRestore, Download, FileUp, Trash2, Upload, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/Button";
import { usePolling } from "@/hooks/usePolling";
import { useNow } from "@/hooks/useNow";
import { apiRequest, errorMessage } from "@/lib/api";
import { exportRoutes } from "@/lib/download";
import { formatRelative, workloadLabel } from "@/lib/format";
import { msg, t, tn } from "@/lib/i18n";
import type { DeletedRoute, ImportItem, RouteStatus } from "@/types/api";

function Dialog({ title, onClose, children, footer, wide }: {
    title: string; onClose: () => void; children: React.ReactNode; footer?: React.ReactNode; wide?: boolean;
}) {
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm" role="dialog" aria-modal="true" aria-label={title}>
            <div className={`bg-gray-800 rounded-xl border border-gray-700 shadow-2xl w-full ${wide ? "max-w-3xl" : "max-w-xl"} max-h-[90vh] flex flex-col`}>
                <div className="px-6 py-4 border-b border-gray-700 flex justify-between items-center gap-3">
                    <h2 className="text-lg font-bold text-white">{title}</h2>
                    <button onClick={onClose} className="text-gray-400 hover:text-white" aria-label={t("Close")}><X className="w-6 h-6" /></button>
                </div>
                <div className="p-6 space-y-4 overflow-y-auto">{children}</div>
                {footer && <div className="px-6 py-4 border-t border-gray-700 flex flex-wrap justify-end items-center gap-2">{footer}</div>}
            </div>
        </div>
    );
}

const routeName = (r: { host: string; deployment: string }) => r.host || workloadLabel(r.deployment);

// ExportDialog downloads some or all routes as a file, to keep as a backup or import elsewhere.
export function ExportDialog({ routes, onClose }: { routes: RouteStatus[]; onClose: () => void }) {
    const exportable = useMemo(() => routes.filter(r => !r.declarative).sort((a, b) => a.namespace.localeCompare(b.namespace) || routeName(a).localeCompare(routeName(b))), [routes]);
    const [selected, setSelected] = useState<Set<string>>(() => new Set(exportable.map(r => r.id)));
    const [busy, setBusy] = useState(false);
    const declarative = routes.length - exportable.length;
    const all = selected.size === exportable.length;

    const toggle = (id: string) => setSelected(prev => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id); else next.add(id);
        return next;
    });

    const run = async () => {
        setBusy(true);
        try {
            await exportRoutes(all ? [] : [...selected]);
            toast.success(tn(selected.size, "{n} route exported", "{n} routes exported"));
            onClose();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    return (
        <Dialog title={t("Export routes")} onClose={onClose} footer={<>
            <Button variant="ghost" onClick={onClose}>{t("Cancel")}</Button>
            <Button onClick={run} disabled={busy || selected.size === 0} className="gap-2">
                <Download size={16} /> {tn(selected.size, "Download {n} route", "Download {n} routes")}
            </Button>
        </>}>
            <p className="text-sm text-gray-300">
                {t("A file with every setting of the routes: dependencies, sleep, schedule, ignored requests, backends, access. Import it to restore them, here or in another Smart Proxy. People and access tokens are not in it.")}
            </p>
            {declarative > 0 && (
                <p className="text-xs text-gray-500">{tn(declarative, "{n} route defined by annotations is left out: it lives in Git already.", "{n} routes defined by annotations are left out: they live in Git already.")}</p>
            )}
            {exportable.length === 0 ? (
                <p className="text-sm text-gray-500 italic">{t("No routes to export.")}</p>
            ) : (
                <div className="space-y-2">
                    <label className="flex items-center gap-2 text-sm text-gray-200">
                        <input type="checkbox" checked={all} onChange={() => setSelected(all ? new Set() : new Set(exportable.map(r => r.id)))} className="accent-blue-500" />
                        {t("All routes")}
                    </label>
                    <ul className="max-h-[45vh] overflow-y-auto divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                        {exportable.map(r => (
                            <li key={r.id}>
                                <label className="flex items-center gap-3 px-3 py-2 cursor-pointer hover:bg-white/[0.03]">
                                    <input type="checkbox" checked={selected.has(r.id)} onChange={() => toggle(r.id)} className="accent-blue-500" />
                                    <span className="font-mono text-sm text-gray-100 truncate">{routeName(r)}{r.path && r.path !== "/" ? r.path : ""}</span>
                                    <span className="ml-auto text-xs text-gray-500 font-mono">{r.namespace}</span>
                                </label>
                            </li>
                        ))}
                    </ul>
                </div>
            )}
        </Dialog>
    );
}

// Readable names of the settings an import changes.
const SETTINGS: Record<string, string> = {
    host: msg("hosts"), path: msg("path"), target_service: msg("service"), target_port: msg("port"), deployment: msg("workload"),
    dependencies: msg("dependencies"), start_in_order: msg("start order"), idle_timeout: msg("sleep after"), always_on: msg("always on"),
    schedule: msg("schedule"), ignore: msg("ignored requests"), when_asleep: msg("answer while asleep"), protection: msg("access"),
    backends: msg("backends"), inject_badge: msg("badge"), path_exact: msg("path"), namespace: msg("namespace"),
};

const STATUS: Record<ImportItem["status"], { label: string; className: string }> = {
    new: { label: msg("New"), className: "bg-green-900/40 text-green-300" },
    changed: { label: msg("Changed"), className: "bg-blue-900/40 text-blue-300" },
    same: { label: msg("Unchanged"), className: "bg-gray-700 text-gray-300" },
    invalid: { label: msg("Can't import"), className: "bg-red-900/40 text-red-300" },
};

// ImportDialog reads a file of routes, shows what importing each would do, then imports those chosen.
export function ImportDialog({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
    const [file, setFile] = useState<{ name: string; text: string } | null>(null);
    const [items, setItems] = useState<ImportItem[] | null>(null);
    const [selected, setSelected] = useState<Set<string>>(new Set());
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const input = useRef<HTMLInputElement>(null);

    const read = async (f: File) => {
        setError(null);
        setItems(null);
        const text = await f.text();
        setFile({ name: f.name, text });
        try {
            const res = await apiRequest("/api/routes/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: text });
            const preview: ImportItem[] = await res.json();
            setItems(preview);
            setSelected(new Set(preview.filter(i => i.status === "new" || i.status === "changed").map(i => i.id)));
        } catch (e) {
            setError(errorMessage(e));
        }
    };

    const run = async () => {
        if (!file) return;
        setBusy(true);
        try {
            const params = new URLSearchParams({ apply: "true", ids: [...selected].join(",") });
            const res = await apiRequest(`/api/routes/import?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: file.text });
            const done: ImportItem[] = await res.json();
            const ok = done.filter(i => i.result === "imported").length;
            const failed = done.filter(i => i.result && i.result !== "imported");
            onImported();
            if (failed.length === 0) {
                toast.success(tn(ok, "{n} route imported", "{n} routes imported"));
                onClose();
            } else {
                setItems(done);
                toast.error(t("{ok} imported, {failed} failed: see why below", { ok, failed: failed.length }));
            }
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    const toggle = (id: string) => setSelected(prev => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id); else next.add(id);
        return next;
    });

    return (
        <Dialog wide title={t("Import routes")} onClose={onClose} footer={items && <>
            <Button variant="ghost" onClick={onClose}>{t("Cancel")}</Button>
            <Button onClick={run} disabled={busy || selected.size === 0} className="gap-2">
                <Upload size={16} /> {tn(selected.size, "Import {n} route", "Import {n} routes")}
            </Button>
        </>}>
            <input ref={input} type="file" accept=".json,application/json" className="hidden"
                onChange={e => { const f = e.target.files?.[0]; if (f) read(f); e.target.value = ""; }} />
            <div
                onClick={() => input.current?.click()}
                onDragOver={e => e.preventDefault()}
                onDrop={e => { e.preventDefault(); const f = e.dataTransfer.files?.[0]; if (f) read(f); }}
                className="flex flex-col items-center justify-center gap-2 px-4 py-6 rounded-lg border-2 border-dashed border-gray-600 hover:border-gray-400 cursor-pointer text-center"
            >
                <FileUp className="w-7 h-7 text-gray-400" />
                <span className="text-sm text-gray-200">{file ? file.name : t("Choose a file exported from Smart Proxy, or drop it here")}</span>
                <span className="text-xs text-gray-500">{t("Nothing changes until you confirm: first you see what each route would do.")}</span>
            </div>
            {error && <p className="text-sm text-red-300">{error}</p>}
            {items && (items.length === 0 ? (
                <p className="text-sm text-gray-500 italic">{t("The file has no routes.")}</p>
            ) : (
                <ul className="divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                    {items.map(i => {
                        const status = STATUS[i.status];
                        const selectable = i.status === "new" || i.status === "changed";
                        return (
                            <li key={i.id} className="px-3 py-2.5 space-y-1">
                                <label className={`flex items-center gap-3 ${selectable ? "cursor-pointer" : ""}`}>
                                    <input type="checkbox" disabled={!selectable} checked={selected.has(i.id)} onChange={() => toggle(i.id)} className="accent-blue-500" />
                                    <span className="font-mono text-sm text-gray-100 truncate">{routeName(i)}{i.path && i.path !== "/" ? i.path : ""}</span>
                                    <span className="text-xs text-gray-500 font-mono">{i.namespace}</span>
                                    <span className={`ml-auto text-[11px] px-1.5 py-0.5 rounded whitespace-nowrap ${status.className}`}>{t(status.label)}</span>
                                </label>
                                <div className="pl-7 space-y-0.5 text-xs">
                                    {(i.changes?.length || 0) > 0 && (
                                        <p className="text-gray-400">{t("Changes: {settings}", { settings: i.changes!.map(c => SETTINGS[c] ? t(SETTINGS[c]) : c).join(", ") })}</p>
                                    )}
                                    {i.problems?.map(p => <p key={p} className="text-red-300">{p}</p>)}
                                    {i.warnings?.map(w => <p key={w} className="text-amber-300 flex items-start gap-1"><AlertTriangle size={12} className="mt-0.5 shrink-0" /> {w}</p>)}
                                    {i.result && i.result !== "imported" && <p className="text-red-300">{i.result}</p>}
                                    {i.result === "imported" && <p className="text-green-300">{t("Imported")}</p>}
                                </div>
                            </li>
                        );
                    })}
                </ul>
            ))}
        </Dialog>
    );
}

// TrashDialog lists the routes deleted in the last 30 days, to restore them.
export function TrashDialog({ onClose, onRestored }: { onClose: () => void; onRestored: () => void }) {
    const { data, refetch } = usePolling<DeletedRoute[]>("/api/routes/deleted", 15000);
    const now = useNow(30000);
    const [busy, setBusy] = useState<string | null>(null);

    const restore = async (d: DeletedRoute) => {
        setBusy(d.id);
        try {
            await apiRequest(`/api/routes/deleted?${new URLSearchParams({ id: d.id })}`, { method: "POST" });
            toast.success(t("{name} restored", { name: routeName(d) }));
            onRestored();
            refetch();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(null);
        }
    };

    const purge = async (d: DeletedRoute) => {
        if (!confirm(t("Delete {name} for good? It can't be restored anymore.", { name: routeName(d) }))) return;
        setBusy(d.id);
        try {
            await apiRequest(`/api/routes/deleted?${new URLSearchParams({ id: d.id })}`, { method: "DELETE" });
            refetch();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(null);
        }
    };

    return (
        <Dialog wide title={t("Recently deleted")} onClose={onClose}>
            <p className="text-sm text-gray-300">
                {t("Deleted routes are kept for 30 days with all their settings, people and tokens included. Restoring one points its Ingresses/Routes at Smart Proxy again.")}
            </p>
            {!data ? (
                <p className="text-sm text-gray-500">{t("Loading…")}</p>
            ) : data.length === 0 ? (
                <p className="text-sm text-gray-500 italic">{t("No deleted routes.")}</p>
            ) : (
                <ul className="divide-y divide-gray-700/60 rounded-lg border border-gray-700/60">
                    {data.map(d => (
                        <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
                            <div className="min-w-0 flex-1 space-y-0.5">
                                <p className="font-mono text-sm text-gray-100 truncate">
                                    {routeName(d)}{d.path && d.path !== "/" ? d.path : ""} <span className="text-xs text-gray-500">{d.namespace}</span>
                                </p>
                                <p className="text-xs text-gray-500">
                                    {d.deleted_by ? t("Deleted {time} by {user}", { time: formatRelative(d.deleted_at, now), user: d.deleted_by }) : t("Deleted {time}", { time: formatRelative(d.deleted_at, now) })}
                                    {(d.resources?.length || 0) > 0 && ` · ${d.resources!.join(", ")}`}
                                    {d.credentials && ` · ${t("with people and tokens")}`}
                                </p>
                            </div>
                            <Button size="sm" variant="secondary" className="gap-1.5" disabled={busy === d.id || d.conflict} onClick={() => restore(d)}
                                title={d.conflict ? t("A route with the same ID exists again: delete it first, or import this one from a file") : undefined}>
                                <ArchiveRestore size={14} /> {t("Restore")}
                            </Button>
                            <Button size="sm" variant="ghost" className="text-gray-400 hover:text-red-300" disabled={busy === d.id} onClick={() => purge(d)} aria-label={t("Delete for good")} title={t("Delete for good")}>
                                <Trash2 size={14} />
                            </Button>
                        </li>
                    ))}
                </ul>
            )}
        </Dialog>
    );
}
