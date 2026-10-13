import { useState, useEffect, useCallback } from "react";
import { handleUnauthorized } from "@/hooks/useAuth";

export function usePolling<T>(url: string, interval = 2000) {
    const [data, setData] = useState<T | null>(null);
    const [error, setError] = useState<Error | null>(null);
    const [loading, setLoading] = useState(true);
    const [fetchedAt, setFetchedAt] = useState(0);

    // State is set when the answer arrives (callbacks), never right away.
    const fetchData = useCallback(() =>
        fetch(url)
            .then(res => {
                if (res.status === 401) handleUnauthorized();
                if (!res.ok) throw new Error(`HTTP error! status: ${res.status}`);
                return res.json();
            })
            .then(json => {
                setData(json);
                setFetchedAt(Date.now());
                setError(null);
            })
            .catch(e => setError(e as Error))
            .finally(() => setLoading(false)), [url]);

    useEffect(() => {
        fetchData();
        const intervalId = setInterval(fetchData, interval);
        return () => clearInterval(intervalId);
    }, [fetchData, interval]);

    return { data, error, loading, fetchedAt, refetch: fetchData };
}
