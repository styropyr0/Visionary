import { useEffect, useRef, useState } from "react";

export interface QueryState<T> {
  data: T | undefined;
  error: Error | undefined;
  /** True while a request is in flight; data keeps the previous result. */
  loading: boolean;
}

let inFlight = 0;

/** True while any query is still loading; auto-refresh waits for it. */
export function queriesBusy(): boolean {
  return inFlight > 0;
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
    inFlight++;
    let counted = true;
    const settle = () => {
      if (counted) inFlight--;
      counted = false;
    };
    fetcherRef
      .current(ctrl.signal)
      .finally(settle)
      .then((data) => {
        if (!ctrl.signal.aborted) setState({ data, error: undefined, loading: false });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setState((s) => ({ data: s.data, error: err instanceof Error ? err : new Error(String(err)), loading: false }));
      });
    return () => {
      settle();
      ctrl.abort();
    };
  }, [key]);

  return state;
}
