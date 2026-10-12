import { apiRequest } from "@/lib/api";

// download fetches a file from the API and saves it, named as the server says.
export async function download(url: string, fallbackName: string) {
    const res = await apiRequest(url);
    const name = res.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] || fallbackName;
    const href = URL.createObjectURL(await res.blob());
    const a = document.createElement("a");
    a.href = href;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(href), 10_000);
}

// exportRoutes downloads routes as a file (all of them when ids is empty).
export function exportRoutes(ids: string[]) {
    const params = ids.length ? `?${new URLSearchParams({ ids: ids.join(",") })}` : "";
    return download(`/api/routes/export${params}`, "smart-proxy-routes.json");
}
