import { toast } from "sonner";
import { t } from "@/lib/i18n";

export function copyText(text: string) {
    navigator.clipboard.writeText(text).then(() => toast.success(t("Copied")), () => toast.error(t("Copy failed")));
}
