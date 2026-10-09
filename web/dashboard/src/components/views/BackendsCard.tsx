import { Copy, GitBranch } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { StatusDot } from "@/components/ui/StatusBadge";
import type { RouteStatus } from "@/types/api";
import { describeCondition, testerLink } from "@/lib/backends";
import { workloadLabel } from "@/lib/format";
import { copyText } from "@/lib/clipboard";

// BackendsCard shows where a route's requests go, and opens the editor.
export function BackendsCard({ route, onEdit }: { route: RouteStatus; onEdit: () => void }) {
    const backends = route.backend_status;
    const edit = (
        <Button variant="secondary" size="sm" className="gap-1.5" onClick={onEdit} disabled={route.declarative}
            title={route.declarative ? "Defined by annotations on its Ingress/Route: edit them there" : undefined}>
            <GitBranch size={14} /> {backends.length > 0 ? "Edit backends" : "Add a backend"}
        </Button>
    );

    if (backends.length === 0) {
        return (
            <Card>
                <CardContent className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 py-4">
                    <div className="text-sm">
                        <p className="text-gray-200">All requests go to <span className="font-mono">{route.target_service}:{route.target_port}</span>.</p>
                        <p className="text-gray-500">Add a backend to try another version with some requests (e.g. a new login provider), or to split traffic.</p>
                    </div>
                    {edit}
                </CardContent>
            </Card>
        );
    }

    return (
        <Card>
            <CardHeader className="flex flex-row items-start justify-between gap-3">
                <div>
                    <CardTitle>Backends</CardTitle>
                    <p className="text-sm text-gray-400">
                        Requests matching a backend's conditions go there; the others are shared by weight among those running.
                    </p>
                </div>
                {edit}
            </CardHeader>
            <CardContent className="p-0">
                <ul className="divide-y divide-gray-700/70">
                    {backends.map(b => {
                        const link = testerLink(route, b.service);
                        return (
                            <li key={b.service} className="px-6 py-3 space-y-1.5 text-sm">
                                <div className="flex flex-wrap items-center justify-between gap-2">
                                    <span className="flex items-center gap-2 min-w-0">
                                        <StatusDot status={b.status} />
                                        <span className="font-mono text-white truncate">{b.service}:{b.port}</span>
                                        {b.workload && <span className="text-xs text-gray-500 truncate">{workloadLabel(b.workload)}</span>}
                                    </span>
                                    <span className="flex items-center gap-2 text-xs shrink-0">
                                        <span className="text-gray-400 tabular-nums">
                                            {b.weight > 0 ? `weight ${b.weight} · ${Math.round(b.share)}% now` : "conditions only"}
                                        </span>
                                        {b.managed
                                            ? <span className="px-1.5 py-0.5 rounded bg-blue-900/40 text-blue-200" title="Woken by requests, sleeps with the app">managed</span>
                                            : <span className="px-1.5 py-0.5 rounded bg-gray-700 text-gray-300" title="Never woken or put to sleep: gets traffic only while it runs">not managed</span>}
                                    </span>
                                </div>
                                {(b.when?.length || 0) > 0 && (
                                    <p className="text-xs text-gray-300">
                                        Gets requests where {b.when!.map(describeCondition).map((d, i) => (
                                            <span key={i}>{i > 0 && <span className="text-gray-500"> or </span>}<span className="font-mono text-blue-200">{d}</span></span>
                                        ))}
                                    </p>
                                )}
                                {link && (b.weight === 0 || (b.when?.length || 0) > 0) && (
                                    <p className="flex items-center gap-1 text-xs text-gray-500 min-w-0">
                                        Try it in a browser: <span className="font-mono text-gray-400 truncate">{link}</span>
                                        <Button variant="ghost" size="icon" className="p-1" onClick={() => copyText(link)} aria-label="Copy link"><Copy size={12} /></Button>
                                    </p>
                                )}
                            </li>
                        );
                    })}
                </ul>
            </CardContent>
        </Card>
    );
}
