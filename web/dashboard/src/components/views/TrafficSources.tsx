import { EyeOff } from "lucide-react";
import { usePolling } from "@/hooks/usePolling";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import type { RouteConfig, RouteStatus, TrafficRules, TrafficSource } from "@/types/api";
import { formatDuration, formatRelative } from "@/lib/format";
import { useNow } from "@/hooks/useNow";

const REASONS: Record<string, string> = {
    "user-agent": "user agent",
    path: "path",
    source: "source IP",
    method: "method",
    "probe-path": "probe path",
};

interface TrafficSourcesProps {
    route: RouteStatus;
    onSave: (route: Partial<RouteConfig>) => Promise<boolean>;
}

// TrafficSources shows who sends requests to a route, so that whatever keeps it awake
// (typically an uptime monitor) can be spotted and ignored in one click.
export function TrafficSources({ route, onSave }: TrafficSourcesProps) {
    const { data: sources, refetch } = usePolling<TrafficSource[]>(`/api/routes/traffic?${new URLSearchParams({ id: route.id })}`, 5000);
    const now = useNow(5000);

    const ignore = async (src: TrafficSource) => {
        const rules: TrafficRules = { ...(route.ignore || {}) };
        if (src.client === "Browser" || src.client === "(no user agent)") {
            rules.paths = [...(rules.paths || []), src.path];
        } else {
            rules.user_agents = [...(rules.user_agents || []), src.client];
        }
        if (await onSave({ ...route, ignore: rules })) refetch();
    };

    const keepingAwake = (sources || []).filter(s => s.counts_as_activity);

    return (
        <Card>
            <CardHeader>
                <CardTitle>Who keeps it awake</CardTitle>
                <p className="text-sm text-gray-400">
                    Clients sending requests in the last 24 hours. Uptime monitors and health checks should be ignored: they then
                    never wake the app, and get an answer from Smart Proxy while it sleeps.
                </p>
            </CardHeader>
            <CardContent className="p-0">
                {!sources ? (
                    <p className="p-6 text-sm text-gray-500">Loading…</p>
                ) : sources.length === 0 ? (
                    <p className="p-6 text-sm text-gray-500">No requests yet.</p>
                ) : (
                    <>
                        {keepingAwake.length > 0 && keepingAwake.every(s => s.client !== "Browser") && (
                            <p className="mx-6 mt-4 text-sm text-yellow-300 bg-yellow-400/10 border border-yellow-400/20 rounded-lg px-3 py-2">
                                Only automated clients are keeping this route awake. If they are monitors, ignore them.
                            </p>
                        )}
                        <ul className="divide-y divide-gray-700/70">
                            {sources.map(src => {
                                const ignorable = src.client !== "Browser" || src.path !== "*";
                                return (
                                    <li key={`${src.client} ${src.method} ${src.path}`} className="grid grid-cols-1 md:grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_auto] gap-x-4 gap-y-1 px-6 py-3 items-center text-sm">
                                        <div className="min-w-0">
                                            <div className="flex items-center gap-2 min-w-0">
                                                <span className="font-medium text-white truncate">{src.client}</span>
                                                {src.counts_as_activity ? (
                                                    <span className="shrink-0 text-[11px] px-1.5 py-0.5 rounded bg-green-900/40 text-green-300">keeps it awake</span>
                                                ) : (
                                                    <span className="shrink-0 text-[11px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-300">
                                                        ignored{src.reason ? ` · ${REASONS[src.reason] || src.reason}` : ""}
                                                    </span>
                                                )}
                                            </div>
                                            <div className="text-xs text-gray-500 truncate" title={src.user_agent}>
                                                {src.method !== "*" && <span className="font-mono">{src.method} {src.path} · </span>}
                                                {src.user_agent || "no user agent"}{src.last_ip ? ` · ${src.last_ip}` : ""}
                                            </div>
                                        </div>
                                        <div className="text-xs text-gray-400 tabular-nums">
                                            {src.requests} request{src.requests === 1 ? "" : "s"}
                                            {src.interval_seconds >= 1 && src.requests > 2 && <> · every ~{formatDuration(src.interval_seconds * 1e9)}</>}
                                            <span className="text-gray-500"> · last {formatRelative(src.last_seen, now)}</span>
                                        </div>
                                        <div className="flex justify-end">
                                            {src.counts_as_activity && ignorable && (
                                                <Button
                                                    variant="secondary"
                                                    size="sm"
                                                    className="gap-1.5"
                                                    onClick={() => ignore(src)}
                                                    disabled={route.declarative}
                                                    title={route.declarative
                                                        ? "Defined by annotations: add smart-proxy/ignore-user-agents or ignore-paths there"
                                                        : src.client === "Browser" || src.client === "(no user agent)" ? `Ignore requests to ${src.path}` : `Ignore requests from ${src.client}`}
                                                >
                                                    <EyeOff size={14} /> Ignore
                                                </Button>
                                            )}
                                        </div>
                                    </li>
                                );
                            })}
                        </ul>
                    </>
                )}
            </CardContent>
        </Card>
    );
}
