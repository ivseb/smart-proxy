import { useEffect, useState, useRef } from "react";
import type { LogEntry } from "@/types/api";
import { t } from "@/lib/i18n";

export function LogsView({ replica }: { replica?: string }) {
    const [logs, setLogs] = useState<LogEntry[]>([]);
    const scrollRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        const eventSource = new EventSource("/api/logs");

        eventSource.onmessage = (event) => {
            try {
                const newLog = JSON.parse(event.data);
                setLogs((prev) => [...prev, newLog].slice(-1000)); // Keep last 1000 logs
            } catch {
                console.error("Failed to parse log", event.data);
            }
        };
        // No onerror close: EventSource reconnects by itself (e.g. after a Smart Proxy restart).

        return () => {
            eventSource.close();
        };
    }, []);

    useEffect(() => {
        if (scrollRef.current) {
            scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
        }
    }, [logs]);

    return (
        <div className="bg-gray-900 rounded-lg border border-gray-700 font-mono text-xs h-[600px] flex flex-col">
            <div className="bg-gray-800 px-4 py-2 border-b border-gray-700 text-gray-400 flex justify-between">
                <span>
                    {t("Live logs")}
                    {replica && <span className="text-gray-500"> · {t("replica")} <span className="text-gray-300">{replica}</span> ({t("each replica streams its own")})</span>}
                </span>
                <button onClick={() => setLogs([])} className="hover:text-white">{t("Clear")}</button>
            </div>
            <div ref={scrollRef} className="flex-1 overflow-y-auto p-4 space-y-1">
                {logs.length === 0 && <div className="text-gray-500 italic">{t("Waiting for logs…")}</div>}
                {logs.map((log, idx) => (
                    <div key={idx} className="flex space-x-2 border-b border-gray-800 pb-0.5 mb-0.5 last:border-0 hover:bg-white/5">
                        <span className="text-gray-500 shrink-0 select-none">[{new Date(log.timestamp).toLocaleTimeString()}]</span>
                        <span className="text-gray-300 break-all">{log.message}</span>
                    </div>
                ))}
            </div>
        </div>
    );
}
