import { useEffect, useState } from "react";

export interface AuthInfo {
    mode: "none" | "basic" | "token" | "oidc" | "header";
    authenticated: boolean;
    user: string;
    logout_url: string;
}

let redirecting = false;

// Called when an API request returns 401: the session expired or was revoked.
// Token and OIDC logins go through /auth/login; basic auth is re-prompted by the browser.
export async function handleUnauthorized() {
    if (redirecting) return;
    try {
        const res = await fetch("/auth/me");
        const info: AuthInfo = await res.json();
        if (info.mode === "token" || info.mode === "oidc") {
            redirecting = true;
            window.location.assign("/auth/login");
        }
    } catch {
        // Ignore: the next poll will retry.
    }
}

export function useAuth() {
    const [info, setInfo] = useState<AuthInfo | null>(null);

    useEffect(() => {
        fetch("/auth/me")
            .then((res) => res.json())
            .then(setInfo)
            .catch(() => setInfo(null));
    }, []);

    return info;
}
