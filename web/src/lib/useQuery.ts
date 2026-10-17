import { useEffect, useRef, useState } from "react";

export interface QueryState<T> {
  data: T | undefined;
  error: Error | undefined;
  /** True while a request is in flight; data keeps the previous result. */
  loading: boolean;
}

/**
 * Runs fetcher whenever key changes. The previous data stays visible while the
 * next request runs, so panels dim instead of flashing a skeleton.
 */
export function useQuery<T>(key: string | null, fetcher: (signal: AbortSignal) => Promise<T>): QueryState<T> {
  const [state, setState] = useState<QueryState<T>>({ data: undefined, error: undefined, loading: key !== null });
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  useEffect(() => {
    if (key === null) {
      setState({ data: undefined, error: undefined, loading: false });
      return;
    }
    const ctrl = new AbortController();
    setState((s) => ({ ...s, loading: true }));
    fetcherRef
      .current(ctrl.signal)
      .then((data) => {
        if (!ctrl.signal.aborted) setState({ data, error: undefined, loading: false });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setState((s) => ({ data: s.data, error: err instanceof Error ? err : new Error(String(err)), loading: false }));
      });
    return () => ctrl.abort();
  }, [key]);

  return state;
}
