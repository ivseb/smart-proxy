

const STATUS_CONFIG = {
    Ready: { icon: "🟢", text: "Ready", color: "text-green-400 bg-green-400/10 border-green-400/20" },
    Scaling: { icon: "🟡", text: "Scaling", color: "text-yellow-400 bg-yellow-400/10 border-yellow-400/20 animate-pulse" },
    Sleep: { icon: "💤", text: "Sleep", color: "text-gray-400 bg-gray-400/10 border-gray-400/20" },
    Error: { icon: "🔴", text: "Error", color: "text-red-400 bg-red-400/10 border-red-400/20" },
    Unknown: { icon: "❓", text: "Unknown", color: "text-gray-500 bg-gray-500/10 border-gray-500/20" },
};

export function StatusBadge({ status }: { status: string }) {
    const config = STATUS_CONFIG[status as keyof typeof STATUS_CONFIG] || STATUS_CONFIG.Unknown;

    return (
        <span className={`inline-flex items-center space-x-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium border ${config.color}`}>
            <span>{config.icon}</span>
            <span>{config.text}</span>
        </span>
    );
}
