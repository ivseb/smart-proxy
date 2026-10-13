import { toast } from "sonner";
import { t } from "@/lib/i18n";

// copyText copies text, with a fallback for pages not served over HTTPS (where browsers don't
// offer the clipboard API), e.g. a dashboard reached at http://smart-proxy.internal.
export function copyText(text: string) {
    const fallback = () => {
        const area = document.createElement("textarea");
        area.value = text;
        area.setAttribute("readonly", "");
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        const ok = document.execCommand("copy");
        area.remove();
        if (ok) toast.success(t("Copied"));
        else toast.error(t("Copy failed"));
    };
    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(() => toast.success(t("Copied")), fallback);
    } else {
        fallback();
    }
}
