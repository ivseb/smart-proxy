import { toast } from "sonner";

export function copyText(text: string) {
    navigator.clipboard.writeText(text).then(() => toast.success("Copied"), () => toast.error("Copy failed"));
}
