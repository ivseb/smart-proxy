const STATUS_CONFIG: Record<string, { label: string; dot: string; badge: string }> = {
    Ready: { label: "Awake", dot: "bg-green-400", badge: "text-green-300 bg-green-400/10 border-green-400/20" },
    Scaling: { label: "Waking", dot: "bg-yellow-400 animate-pulse", badge: "text-yellow-300 bg-yellow-400/10 border-yellow-400/20" },
    Sleep: { label: "Asleep", dot: "bg-slate-400", badge: "text-slate-300 bg-slate-400/10 border-slate-400/20" },
    Error: { label: "Error", dot: "bg-red-400", badge: "text-red-300 bg-red-400/10 border-red-400/20" },
    Unwatched: { label: "Not managed", dot: "bg-orange-400", badge: "text-orange-300 bg-orange-400/10 border-orange-400/20" },
    Offline: { label: "Offline", dot: "bg-gray-500", badge: "text-gray-400 bg-gray-500/10 border-gray-500/20" },
};

const UNKNOWN = { label: "Unknown", dot: "bg-gray-500", badge: "text-gray-400 bg-gray-500/10 border-gray-500/20" };

export function StatusDot({ status, className = "" }: { status: string; className?: string }) {
    const config = STATUS_CONFIG[status] || UNKNOWN;
    return <span className={`inline-block w-2 h-2 rounded-full shrink-0 ${config.dot} ${className}`} title={config.label} />;
}

export function StatusBadge({ status }: { status: string }) {
    const config = STATUS_CONFIG[status] || UNKNOWN;
    return (
        <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium border whitespace-nowrap ${config.badge}`}>
            <StatusDot status={status} />
            {config.label}
        </span>
    );
}
