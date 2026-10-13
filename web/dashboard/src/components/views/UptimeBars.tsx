import type { UptimeDay } from "@/types/api";
import { dayTitle, upShare } from "@/lib/uptime";

// UptimeBars draws one bar per day: its height is the share of the day with at least one replica.
export function UptimeBars({ days, height = 40, className = "" }: { days: UptimeDay[]; height?: number; className?: string }) {
    return (
        <div className={`flex items-end gap-px ${className}`} style={{ height }}>
            {days.map(d => {
                const share = upShare(d);
                return (
                    <div key={d.date} title={dayTitle(d.date, d)} className="flex-1 min-w-[3px] h-full flex items-end rounded-sm bg-gray-700/40">
                        {share === null ? (
                            <div className="w-full h-px bg-gray-600" />
                        ) : (
                            <div className={`w-full rounded-sm ${share > 0 ? "bg-green-500/80" : ""}`} style={{ height: `${Math.max(share > 0 ? 6 : 0, share * 100)}%` }} />
                        )}
                    </div>
                );
            })}
        </div>
    );
}
