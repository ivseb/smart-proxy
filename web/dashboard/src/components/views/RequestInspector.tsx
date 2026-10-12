import { Fragment, useMemo, useState } from "react";
import { toast } from "sonner";
import { ArrowUp, ChevronDown, ChevronRight, Circle, EyeOff, Pause, Play, Search, Square } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { usePolling } from "@/hooks/usePolling";
import { useNow } from "@/hooks/useNow";
import { useStoredState } from "@/hooks/useStoredState";
import { apiRequest, errorMessage } from "@/lib/api";
import { formatDuration } from "@/lib/format";
import { userAgentToken } from "@/lib/rules";
import { msg, t, tn, useLang } from "@/lib/i18n";
import type { IgnoreKind, InspectedRequest, InspectedRequests, RequestOutcome, RouteStatus } from "@/types/api";

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
    paused: msg("route paused"),
};

const IGNORED: Record<string, string> = {
    "user-agent": msg("monitor (user agent)"),
    path: msg("ignored path"),
    source: msg("ignored source IP"),
    method: msg("ignored method"),
    "probe-path": msg("health check path"),
    rule: msg("ignore rule"),
};

// Quick views of the recorded requests.
type View = "all" | "counted" | "problems" | "ignored";
const VIEWS: { value: View; label: string; title: string }[] = [
    { value: "all", label: msg("All"), title: msg("Every recorded request") },
    { value: "counted", label: msg("Without ignored"), title: msg("Hide monitors, health checks and requests matching the ignore rules") },
    { value: "problems", label: msg("Problems"), title: msg("Errors, refused requests and requests the app couldn't answer") },
    { value: "ignored", label: msg("Ignored only"), title: msg("Only requests that don't count as activity") },
];

const inView = (r: InspectedRequest, view: View) => {
    switch (view) {
        case "counted": return !r.ignored;
        case "ignored": return !!r.ignored;
        case "problems": return r.status >= 400 || r.outcome === "unavailable" || r.outcome === "error" || r.outcome === "denied";
        default: return true;
    }
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

// A request's identity across refreshes (older Smart Proxy versions sent no id).
const keyOf = (r: InspectedRequest) => r.id || `${r.replica}-${r.at}-${r.method}-${r.path}`;

// matches applies the search: every word must appear somewhere in the request; a word starting
// with "-" must not.
function matches(r: InspectedRequest, words: string[]): boolean {
    if (words.length === 0) return true;
    const fields = [r.method, r.path, r.client, r.user_agent, r.backend, r.user, String(r.status),
        OUTCOMES[r.outcome] && t(OUTCOMES[r.outcome].label), ...Object.entries(r.headers).flat(), ...(r.cookies || []), ...(r.query || [])]
        .filter((v): v is string => !!v).map(v => v.toLowerCase());
    return words.every(w => w.startsWith("-") && w.length > 1
        ? !fields.some(v => v.includes(w.slice(1)))
        : fields.some(v => v.includes(w)));
}

interface RequestInspectorProps {
    route: RouteStatus;
    onChanged: () => void;
    // Offered on each header of a recorded request (backend conditions).
    headerAction?: (name: string, value: string) => React.ReactNode;
    // Adds an ignore rule to the route (absent when its rules can't be edited here).
    onIgnore?: (kind: IgnoreKind, value: string) => void;
}

// RequestInspector records the requests reaching a route for a while and shows them: what was
// sent (headers, cookie and parameter names) and what Smart Proxy did with each. While a request
// is open, or the list is paused, the list holds still: new requests are counted, not shown.
export function RequestInspector({ route, onChanged, headerAction, onIgnore }: RequestInspectorProps) {
    const now = useNow(1000);
    const until = route.inspect_until ? new Date(route.inspect_until).getTime() : 0;
    const recording = until > now;
    const [paused, setPaused] = useState(false);
    const [filter, setFilter] = useState("");
    const [view, setView] = useStoredState<View>("inspector.view", "all");
    const [backend, setBackend] = useState("");
    const [open, setOpen] = useState<string | null>(null);
    const [minutes, setMinutes] = useState(15);
    const [busy, setBusy] = useState(false);

    const url = `/api/routes/requests?${new URLSearchParams({ id: route.id })}`;
    const { data } = usePolling<InspectedRequests>(url, recording ? 2000 : 30000);
    // The requests shown while held (paused, or a request open).
    const [held, setHeld] = useState<InspectedRequests | null>(null);
    const shown = held ?? data;

    const lang = useLang(); // The search also matches the outcomes' translated labels
    const all = useMemo(() => [...(shown?.requests || [])].reverse(), [shown]); // Newest first
    const backends = useMemo(() => [...new Set(all.map(r => r.backend).filter((b): b is string => !!b))].sort(), [all]);
    const requests = useMemo(() => {
        const words = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
        return all.filter(r => inView(r, view) && (!backend || r.backend === backend) && matches(r, words));
        // eslint-disable-next-line react-hooks/exhaustive-deps -- t() depends on lang
    }, [all, filter, view, backend, lang]);
    const counts = useMemo(() => Object.fromEntries(VIEWS.map(v => [v.value, all.filter(r => inView(r, v.value)).length])), [all]);

    const fresh = useMemo(() => {
        if (!held || !data) return 0;
        const seen = new Set(held.requests.map(keyOf));
        return data.requests.filter(r => !seen.has(keyOf(r))).length;
    }, [held, data]);

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

    const toggle = (id: string) => {
        if (open === id) {
            setOpen(null);
            if (!paused) setHeld(null);
        } else {
            setOpen(id);
            if (!held) setHeld(data);
        }
    };

    const togglePause = () => {
        if (paused) {
            setPaused(false);
            if (!open) setHeld(null);
        } else {
            setPaused(true);
            if (!held) setHeld(data);
        }
    };

    const exclude = (path: string) => {
        setFilter(f => `${f.trim()} -${path}`.trim());
        setOpen(null);
        if (!paused) setHeld(null);
    };

    const hasRequests = (shown?.requests.length || 0) > 0;

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
                {hasRequests && (
                    <div className="space-y-2 px-6 pb-3">
                        <div className="flex flex-col lg:flex-row lg:items-center gap-2">
                            <div className="relative flex-1">
                                <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-500" />
                                <input
                                    value={filter}
                                    onChange={e => setFilter(e.target.value)}
                                    placeholder={t("Search path, header, IP, status… (-word to exclude)")}
                                    title={t("Words separated by spaces must all match; a word starting with - hides the requests containing it, e.g. -/health")}
                                    className="w-full bg-gray-900 border border-gray-700 rounded-lg pl-8 pr-3 py-1.5 text-sm text-gray-200 placeholder:text-gray-500"
                                />
                            </div>
                            <div className="flex flex-wrap items-center gap-2">
                                <div className="flex bg-gray-900 border border-gray-700 rounded-lg p-0.5" role="group" aria-label={t("Show")}>
                                    {VIEWS.map(v => (
                                        <button key={v.value} onClick={() => setView(v.value)} title={t(v.title)}
                                            className={`px-2 py-1 rounded-md text-xs whitespace-nowrap transition-colors ${view === v.value ? "bg-gray-700 text-white" : "text-gray-400 hover:text-white"}`}>
                                            {t(v.label)} <span className="tabular-nums text-gray-500">{counts[v.value]}</span>
                                        </button>
                                    ))}
                                </div>
                                {backends.length > 1 && (
                                    <select value={backend} onChange={e => setBackend(e.target.value)} aria-label={t("Backend")}
                                        className="bg-gray-900 border border-gray-700 rounded-lg px-2 py-1 text-xs text-gray-200">
                                        <option value="">{t("All backends")}</option>
                                        {backends.map(b => <option key={b} value={b}>{b}</option>)}
                                    </select>
                                )}
                            </div>
                        </div>
                        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
                            <span className="tabular-nums">
                                {requests.length === all.length
                                    ? tn(all.length, "{n} request", "{n} requests")
                                    : t("{shown} of {total} requests", { shown: requests.length, total: all.length })}
                                {(shown?.replicas || 1) > 1 && ` · ${t("from {n} replicas", { n: shown!.replicas })}`}
                            </span>
                            {held && (
                                <span className="flex items-center gap-2">
                                    <span className="text-yellow-300/90">{open && !paused ? t("Held while a request is open") : t("Paused")}</span>
                                    {fresh > 0 && (
                                        <button onClick={() => setHeld(data)} className="flex items-center gap-1 px-2 py-0.5 rounded-full bg-blue-600/20 text-blue-200 hover:bg-blue-600/30">
                                            <ArrowUp size={12} /> {tn(fresh, "{n} new request", "{n} new requests")}
                                        </button>
                                    )}
                                </span>
                            )}
                            {recording && (
                                <Button variant="ghost" size="sm" className="gap-1.5 ml-auto" onClick={togglePause}>
                                    {paused ? <><Play size={12} /> {t("Resume")}</> : <><Pause size={12} /> {t("Pause")}</>}
                                </Button>
                            )}
                        </div>
                    </div>
                )}

                {!shown ? (
                    <p className="px-6 pb-6 text-sm text-gray-500">{t("Loading…")}</p>
                ) : !hasRequests ? (
                    <p className="px-6 pb-6 text-sm text-gray-500">
                        {recording ? t("Waiting for requests… Open the application or send a request to see it here.")
                            : t("Nothing recorded. Start recording, then use the application: each request shows up here.")}
                    </p>
                ) : requests.length === 0 ? (
                    <p className="px-6 pb-6 text-sm text-gray-500">{t("No request matches these filters.")}</p>
                ) : (
                    <div className="max-h-[70vh] overflow-auto border-t border-gray-700/70">
                        <table className="w-full text-sm">
                            <thead className="sticky top-0 z-10 bg-gray-800 shadow-[0_1px_0_0_rgb(55_65_81_/_0.7)]">
                                <tr className="text-left text-xs text-gray-500 whitespace-nowrap">
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
                                {requests.map(r => {
                                    const id = keyOf(r);
                                    const expanded = open === id;
                                    const outcome = OUTCOMES[r.outcome] || { label: r.outcome, className: "bg-gray-700 text-gray-300" };
                                    return (
                                        <Fragment key={id}>
                                            <tr className={`cursor-pointer ${expanded ? "bg-blue-500/10" : "hover:bg-white/[0.03]"}`} onClick={() => toggle(id)}>
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
                                                        <RequestDetails request={r} headerAction={headerAction} onIgnore={onIgnore} onExclude={exclude} />
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

function RequestDetails({ request: r, headerAction, onIgnore, onExclude }: {
    request: InspectedRequest;
    headerAction?: RequestInspectorProps["headerAction"];
    onIgnore?: RequestInspectorProps["onIgnore"];
    onExclude: (path: string) => void;
}) {
    const facts: [string, string | undefined][] = [
        [t("Client"), r.client],
        [t("User"), r.user],
        [t("Host"), r.host],
        [t("User agent"), r.user_agent],
        [t("Served by"), r.replica],
    ];
    const headers = Object.entries(r.headers).sort(([a], [b]) => a.localeCompare(b));
    const agent = userAgentToken(r.user_agent);
    const chip = "px-2 py-1 rounded-md bg-gray-800 border border-gray-700 hover:border-gray-500 hover:text-white transition-colors";
    return (
        <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2 text-xs text-gray-400">
                <button className={`${chip} flex items-center gap-1.5`} onClick={() => onExclude(r.path)} title={t("Hide the requests to this path from the list (not from the app)")}>
                    <EyeOff size={12} /> {t("Hide {path} here", { path: r.path })}
                </button>
                {onIgnore && !r.ignored && (
                    <>
                        <span className="ml-2">{t("Don't count as activity:")}</span>
                        <button className={chip} onClick={() => onIgnore("paths", r.path)}>{t("path {value}", { value: r.path })}</button>
                        {agent && <button className={chip} onClick={() => onIgnore("user_agents", agent)}>{t("user agent {value}", { value: agent })}</button>}
                        {r.client && <button className={chip} onClick={() => onIgnore("sources", r.client)}>{t("IP {value}", { value: r.client })}</button>}
                    </>
                )}
            </div>
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
        </div>
    );
}
