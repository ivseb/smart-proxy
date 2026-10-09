// apiRequest performs a dashboard API call and throws with the server's message on HTTP errors,
// since fetch only rejects on network failures.
export async function apiRequest(url: string, init?: RequestInit): Promise<Response> {
    const res = await fetch(url, init);
    if (!res.ok) {
        const message = (await res.text()).trim();
        throw new Error(message || `HTTP ${res.status}`);
    }
    return res;
}

export function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
}
