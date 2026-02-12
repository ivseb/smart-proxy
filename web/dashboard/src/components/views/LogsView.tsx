import { useEffect, useState, useRef } from "react";
import type { LogEntry } from "@/types/api";

export function LogsView() {
    const [logs, setLogs] = useState<LogEntry[]>([]);
    const scrollRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        const eventSource = new EventSource("/api/logs");

        eventSource.onmessage = (event) => {
            try {
                const newLog = JSON.parse(event.data);
                setLogs((prev) => [...prev, newLog].slice(-1000)); // Keep last 1000 logs
            } catch (e) {
                console.error("Failed to parse log", event.data);
            }
        };

        eventSource.onerror = (e) => {
            console.error("EventSource failed", e);
            eventSource.close();
            // Reconnect logic could go here
        };

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
                <span>System Logs (Live)</span>
                <button onClick={() => setLogs([])} className="hover:text-white">Clear</button>
            </div>
            <div ref={scrollRef} className="flex-1 overflow-y-auto p-4 space-y-1">
                {logs.length === 0 && <div className="text-gray-500 italic">Waiting for logs...</div>}
                {logs.map((log, idx) => (
                    <div key={idx} className="flex space-x-2 border-b border-gray-800 pb-0.5 mb-0.5 last:border-0 hover:bg-white/5">
                        <span className="text-gray-500 shrink-0 select-none">[{new Date(log.timestamp).toLocaleTimeString()}]</span>
                        <span className={`uppercase font-bold shrink-0 w-16 ${log.level === 'ERROR' ? 'text-red-500' :
                            log.level === 'WARN' ? 'text-yellow-500' : 'text-blue-500'
                            }`}>{log.level}</span>
                        <span className="text-gray-300 break-all">{log.message}</span>
                    </div>
                ))}
            </div>
        </div>
    );
}
