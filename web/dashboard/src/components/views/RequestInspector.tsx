import { Fragment, useMemo, useState } from "react";
import { toast } from "sonner";
import { ChevronDown, ChevronRight, Circle, Pause, Play, Search, Square } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { usePolling } from "@/hooks/usePolling";
import { useNow } from "@/hooks/useNow";
import { apiRequest, errorMessage } from "@/lib/api";
import { formatDuration } from "@/lib/format";
import { msg, t, tn, useLang } from "@/lib/i18n";
import type { InspectedRequest, InspectedRequests, RequestOutcome, RouteStatus } from "@/types/api";

// What happened to a request, in words, with a color.
const OUTCOMES: Record<RequestOutcome, { label: string; className: string }> = {
    proxied: { label: msg("Answered by the app"), className: "bg-green-900/40 text-green-300" },
    woken: { label: msg("Woke the app, then answered"), className: "bg-blue-900/40 text-blue-300" },
    waking_page: { label: msg("Waking page shown"), className: "bg-yellow-900/40 text-yellow-300" },
    asleep: { label: msg("Answered while asleep"), className: "bg-gray-700 text-gray-300" },
    unavailable: { label: msg("App didn't start in time"), className: "bg-red-900/40 text-red-300" },
    login: { label: msg("Sent to login"), className: "bg-purple-900/40 text-purple-300" },
    denied: { label: msg("Denied"), className: "bg-orange-900/40 text-orange-300" },
    error: { label: msg("App unreachable"), className: "bg-red-900/40 text-red-300" },
};

const WHY: Record<string, string> = {
    split: msg("by weight"),
    sticky: msg("same as before (cookie)"),
    condition: msg("matched a condition"),
    link: msg("chosen with the link"),
};

const IGNORED: Record<string, string> = {
    "user-agent": msg("monitor (user agent)"),
    path: msg("ignored path"),
    source: msg("ignored source IP"),
    method: msg("ignored method"),
    "probe-path": msg("health check path"),
};

const DURATIONS = [5, 15, 60];

// Headers that differ on every request or only describe the hop: useless as backend conditions.
const PER_REQUEST = new Set(["content-length", "content-type", "accept-encoding", "connection", "x-request-id",
    "x-forwarded-for", "x-forwarded-port", "x-forwarded-proto", "x-forwarded-scheme", "x-forwarded-host", "x-real-ip",
    "x-scheme", "x-original-forwarded-for", "traceparent", "tracestate", "x-b3-traceid", "x-b3-spanid", "x-b3-sampled"]);

function statusClass(status: number): string {
    if (status >= 500) return "text-red-300";
    if (status >= 400) return "text-orange-300";
    if (status >= 300) return "text-blue-300";
    return "text-green-300";
}

function time(at: string): string {
    return new Date(at).toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

interface RequestInspectorProps {
    route: RouteStatus;
    onChanged: () => void;
    // Offered on each header of a recorded request (backend conditions).
    headerAction?: (name: string, value: string) => React.ReactNode;
}

// RequestInspector records the requests reaching a route for a while and shows them: what was
// sent (headers, cookie and parameter names) and what Smart Proxy did with each.
export function RequestInspector({ route, onChanged, headerAction }: RequestInspectorProps) {
    const now = useNow(1000);
    const until = route.inspect_until ? new Date(route.inspect_until).getTime() : 0;
    const recording = until > now;
    const [paused, setPaused] = useState(false);
    const [filter, setFilter] = useState("");
    const [open, setOpen] = useState<string | null>(null);
    const [minutes, setMinutes] = useState(15);
    const [busy, setBusy] = useState(false);

    const url = `/api/routes/requests?${new URLSearchParams({ id: route.id })}`;
    const { data } = usePolling<InspectedRequests>(url, recording && !paused ? 2000 : 30000);
    const [frozen, setFrozen] = useState<InspectedRequests | null>(null);
    const shown = paused ? frozen : data;

    const lang = useLang(); // The filter also matches the outcomes' translated labels
    const requests = useMemo(() => {
        const list = [...(shown?.requests || [])].reverse(); // Newest first
        const f = filter.trim().toLowerCase();
        if (!f) return list;
        return list.filter(r =>
            [r.method, r.path, r.client, r.user_agent, r.backend, r.user, String(r.status), OUTCOMES[r.outcome]?.label, OUTCOMES[r.outcome] && t(OUTCOMES[r.outcome].label),
                ...Object.entries(r.headers).flat(), ...(r.cookies || []), ...(r.query || [])]
                .some(v => v && v.toLowerCase().includes(f)));
        // eslint-disable-next-line react-hooks/exhaustive-deps -- t() depends on lang
    }, [shown, filter, lang]);

    const setRecording = async (mins: number) => {
        setBusy(true);
        try {
            await apiRequest(`/api/routes/inspect?${new URLSearchParams({ id: route.id, minutes: String(mins) })}`, { method: "POST" });
            toast.success(mins > 0 ? t("Recording requests for {n} minutes", { n: mins }) : t("Recording stopped"));
            onChanged();
        } catch (e) {
            toast.error(t("Failed: {error}", { error: errorMessage(e) }));
        } finally {
            setBusy(false);
        }
    };

    const togglePause = () => {
        setFrozen(paused ? null : data);
        setPaused(!paused);
    };

    const key = (r: InspectedRequest, i: number) => `${r.replica}-${r.at}-${i}`;

    return (
        <Card>
            <CardHeader className="flex flex-col md:flex-row md:items-start justify-between gap-3">
                <div className="space-y-1">
                    <CardTitle>{t("Requests")}</CardTitle>
                    <p className="text-sm text-gray-400">
                        {t("Record what reaches this route for a while: who sends it, its headers, cookie and parameter names, and what Smart Proxy did with it. Credentials are masked; bodies are never recorded.")}
                    </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                    {recording ? (
                        <>
                            <span className="flex items-center gap-1.5 text-sm text-red-300">
                                <Circle size={10} className="fill-red-500 text-red-500 animate-pulse" />
                                {t("Recording · {time} left", { time: formatDuration((until - now) * 1e6) })}
                            </span>
                            <Button variant="secondary" size="sm" className="gap-1.5" onClick={() => setRecording(0)} disabled={busy}>
                                <Square size={12} /> {t("Stop")}
                            </Button>
                        </>
                    ) : (
                        <>
                            <select
                                value={minutes}
                                onChange={e => setMinutes(Number(e.target.value))}
                                className="bg-gray-900 border border-gray-700 rounded-lg px-2 py-1.5 text-sm text-gray-200"
                                aria-label={t("Recording duration")}
                            >
                                {DURATIONS.map(m => <option key={m} value={m}>{m} min</option>)}
                            </select>
                            <Button size="sm" className="gap-1.5" onClick={() => setRecording(minutes)} disabled={busy}>
                                <Circle size={10} className="fill-current" /> {t("Record")}
                            </Button>
                        </>
                    )}
                </div>
            </CardHeader>
            <CardContent className="p-0">
                {(shown?.requests.length || 0) > 0 && (
                    <div className="flex flex-col sm:flex-row sm:items-center gap-2 px-6 pb-3">
                        <div className="relative flex-1">
                            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-500" />
                            <input
                                value={filter}
                                onChange={e => setFilter(e.target.value)}
                                placeholder={t("Filter by path, header, IP, status…")}
                                className="w-full bg-gray-900 border border-gray-700 rounded-lg pl-8 pr-3 py-1.5 text-sm text-gray-200 placeholder:text-gray-500"
                            />
                        </div>
                        <span className="text-xs text-gray-500 tabular-nums">
                            {tn(requests.length, "{n} request", "{n} requests")}
                            {(shown?.replicas || 1) > 1 && ` · ${t("from {n} replicas", { n: shown!.replicas })}`}
                        </span>
                        {recording && (
                            <Button variant="ghost" size="sm" className="gap-1.5" onClick={togglePause}>
                                {paused ? <><Play size={12} /> {t("Resume")}</> : <><Pause size={12} /> {t("Pause")}</>}
                            </Button>
                        )}
                    </div>
                )}

                {!shown ? (
                    <p className="px-6 pb-6 text-sm text-gray-500">{t("Loading…")}</p>
                ) : shown.requests.length === 0 ? (
                    <p className="px-6 pb-6 text-sm text-gray-500">
                        {recording ? t("Waiting for requests… Open the application or send a request to see it here.")
                            : t("Nothing recorded. Start recording, then use the application: each request shows up here.")}
                    </p>
                ) : (
                    <div className="overflow-x-auto">
                        <table className="w-full text-sm">
                            <thead>
                                <tr className="text-left text-xs text-gray-500 border-y border-gray-700/70 whitespace-nowrap">
                                    <th className="pl-6 pr-2 py-2 font-medium w-6" />
                                    <th className="px-2 py-2 font-medium">{t("Time")}</th>
                                    <th className="px-2 py-2 font-medium">{t("Request")}</th>
                                    <th className="px-2 py-2 font-medium">{t("Status")}</th>
                                    <th className="px-2 py-2 font-medium">{t("What happened")}</th>
                                    <th className="px-2 py-2 font-medium">{t("Backend")}</th>
                                    <th className="pl-2 pr-6 py-2 font-medium text-right whitespace-nowrap">{t("Time taken")}</th>
                                </tr>
                            </thead>
                            <tbody className="divide-y divide-gray-700/50">
                                {requests.map((r, i) => {
                                    const id = key(r, i);
                                    const expanded = open === id;
                                    const outcome = OUTCOMES[r.outcome] || { label: r.outcome, className: "bg-gray-700 text-gray-300" };
                                    return (
                                        <Fragment key={id}>
                                            <tr className="hover:bg-white/[0.03] cursor-pointer" onClick={() => setOpen(expanded ? null : id)}>
                                                <td className="pl-6 pr-2 py-2 text-gray-500">{expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</td>
                                                <td className="px-2 py-2 text-gray-400 tabular-nums whitespace-nowrap">{time(r.at)}</td>
                                                <td className="px-2 py-2 max-w-[28rem]">
                                                    <div className="flex items-center gap-2 min-w-0">
                                                        <span className="shrink-0 font-mono text-xs px-1.5 py-0.5 rounded bg-gray-700 text-gray-200">{r.method}</span>
                                                        <span className="font-mono text-gray-100 truncate" title={r.path}>{r.path}</span>
                                                        {(r.query?.length || 0) > 0 && <span className="shrink-0 text-xs text-gray-500">?{r.query!.join("&")}</span>}
                                                    </div>
                                                </td>
                                                <td className={`px-2 py-2 tabular-nums font-medium ${statusClass(r.status)}`}>{r.status || "—"}</td>
                                                <td className="px-2 py-2">
                                                    <span className={`text-[11px] px-1.5 py-0.5 rounded whitespace-nowrap ${outcome.className}`}>{t(outcome.label)}</span>
                                                    {r.ignored && <span className="ml-1.5 text-[11px] text-gray-500 whitespace-nowrap">{t("doesn't count")} · {IGNORED[r.ignored] ? t(IGNORED[r.ignored]) : r.ignored}</span>}
                                                </td>
                                                <td className="px-2 py-2 text-gray-300 whitespace-nowrap">
                                                    {r.backend ? <span className="font-mono text-xs">{r.backend}</span> : <span className="text-gray-600">—</span>}
                                                    {r.why && <span className="text-xs text-gray-500"> · {WHY[r.why] ? t(WHY[r.why]) : r.why}</span>}
                                                </td>
                                                <td className="pl-2 pr-6 py-2 text-right text-gray-400 tabular-nums whitespace-nowrap">
                                                    {r.duration_ms < 1000 ? `${Math.round(r.duration_ms)} ms` : `${(r.duration_ms / 1000).toFixed(1)} s`}
                                                </td>
                                            </tr>
                                            {expanded && (
                                                <tr className="bg-gray-900/60">
                                                    <td colSpan={7} className="px-6 py-4">
                                                        <RequestDetails request={r} headerAction={headerAction} />
                                                    </td>
                                                </tr>
                                            )}
                                        </Fragment>
                                    );
                                })}
                            </tbody>
                        </table>
                    </div>
                )}
            </CardContent>
        </Card>
    );
}

function RequestDetails({ request: r, headerAction }: { request: InspectedRequest; headerAction?: RequestInspectorProps["headerAction"] }) {
    const facts: [string, string | undefined][] = [
        [t("Client"), r.client],
        [t("User"), r.user],
        [t("Host"), r.host],
        [t("User agent"), r.user_agent],
        [t("Served by"), r.replica],
    ];
    const headers = Object.entries(r.headers).sort(([a], [b]) => a.localeCompare(b));
    return (
        <div className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-6 text-sm">
            <div className="space-y-3">
                <dl className="space-y-1.5">
                    {facts.filter(([, v]) => v).map(([k, v]) => (
                        <div key={k} className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-2">
                            <dt className="text-gray-500">{k}</dt>
                            <dd className="text-gray-200 break-all">{v}</dd>
                        </div>
                    ))}
                </dl>
                {(r.cookies?.length || 0) > 0 && (
                    <div>
                        <p className="text-xs text-gray-500 mb-1">{t("Cookies (names only)")}</p>
                        <div className="flex flex-wrap gap-1">
                            {r.cookies!.map(c => <span key={c} className="font-mono text-xs px-1.5 py-0.5 rounded bg-gray-800 text-gray-300">{c}</span>)}
                        </div>
                    </div>
                )}
                {(r.query?.length || 0) > 0 && (
                    <div>
                        <p className="text-xs text-gray-500 mb-1">{t("Query parameters (names only)")}</p>
                        <div className="flex flex-wrap gap-1">
                            {r.query!.map(q => <span key={q} className="font-mono text-xs px-1.5 py-0.5 rounded bg-gray-800 text-gray-300">{q}</span>)}
                        </div>
                    </div>
                )}
            </div>
            <div className="min-w-0">
                <p className="text-xs text-gray-500 mb-1">{t("Headers")}</p>
                <table className="w-full text-xs">
                    <tbody className="divide-y divide-gray-800">
                        {headers.map(([name, value]) => (
                            <tr key={name} className="align-top group">
                                <td className="py-1 pr-3 font-mono text-gray-400 whitespace-nowrap">{name}</td>
                                <td className="py-1 font-mono text-gray-200 break-all">{value}</td>
                                {headerAction && (
                                    <td className="py-1 pl-2 text-right whitespace-nowrap opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
                                        {!PER_REQUEST.has(name.toLowerCase()) && headerAction(name, value)}
                                    </td>
                                )}
                            </tr>
                        ))}
                    </tbody>
                </table>
            </div>
        </div>
    );
}
