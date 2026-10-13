import { Copy } from "lucide-react";
import { copyText } from "@/lib/clipboard";
import { t } from "@/lib/i18n";

// Copyable shows a value that may be cut short (full value on hover) with a button copying all of
// it, shown on hover or focus. Clicking the button doesn't reach the row it sits in.
export function Copyable({ value, children, className = "", wrap }: {
    value: string;
    children?: React.ReactNode;
    className?: string;
    wrap?: boolean; // Break long values instead of cutting them
}) {
    return (
        <span className={`group/copy inline-flex items-center gap-1 min-w-0 max-w-full ${className}`}>
            <span className={wrap ? "break-all" : "truncate"} title={value}>{children ?? value}</span>
            <button
                type="button"
                onClick={e => { e.stopPropagation(); copyText(value); }}
                className="shrink-0 p-0.5 rounded text-gray-500 hover:text-white hover:bg-white/10 opacity-0 group-hover/copy:opacity-100 focus:opacity-100 transition-opacity"
                aria-label={t("Copy")}
                title={t("Copy")}
            >
                <Copy size={12} />
            </button>
        </span>
    );
}
