import { useEffect, useState } from "react";

// useStoredState is useState remembered in localStorage (a per-browser convenience, e.g. filters).
// Storage can be unavailable (private mode, blocked site data), so it silently falls back to memory.
export function useStoredState<T>(key: string, initial: T): [T, (value: T) => void] {
    const [value, setValue] = useState<T>(() => {
        try {
            const raw = window.localStorage.getItem(key);
            return raw === null ? initial : (JSON.parse(raw) as T);
        } catch {
            return initial;
        }
    });

    useEffect(() => {
        try {
            window.localStorage.setItem(key, JSON.stringify(value));
        } catch {
            // Not persisted; the in-memory value still works.
        }
    }, [key, value]);

    return [value, setValue];
}
