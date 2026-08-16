import { useCallback, useEffect, useRef, useState } from 'react';

export interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  reload: () => void;
}

/**
 * Runs an async loader and keeps its result, with optional polling.
 * Results from a superseded request are discarded, so a slow response can never
 * overwrite a newer one.
 */
export function useApi<T>(loader: () => Promise<T>, deps: unknown[] = [], pollMs = 0): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);

  const requestId = useRef(0);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    const current = ++requestId.current;

    const run = async (showSpinner: boolean) => {
      if (showSpinner) setLoading(true);
      try {
        const result = await loaderRef.current();
        if (!cancelled && current === requestId.current) {
          setData(result);
          setError(null);
        }
      } catch (err) {
        if (!cancelled && current === requestId.current) {
          setError(err instanceof Error ? err.message : String(err));
        }
      } finally {
        if (!cancelled && current === requestId.current) {
          setLoading(false);
        }
      }
    };

    void run(true);

    if (pollMs > 0) {
      const timer = window.setInterval(() => void run(false), pollMs);
      return () => {
        cancelled = true;
        window.clearInterval(timer);
      };
    }
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, pollMs]);

  return { data, loading, error, reload };
}
