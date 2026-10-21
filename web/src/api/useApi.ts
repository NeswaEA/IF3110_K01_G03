import { useCallback, useEffect, useRef, useState } from "react";
import { isAbortError, request } from "./client";
import type { ApiResponse } from "./types";

export type Query<T> =
  | { status: "loading" }
  | { status: "error"; error: unknown }
  | { status: "success"; response: ApiResponse<T> };

/**
 * GETs path and re-fetches when path changes or reload() is called. A response
 * for an outdated path is dropped (the request is aborted).
 */
export function useApi<T>(path: string | null): [Query<T>, () => void] {
  const [state, setState] = useState<Query<T>>({ status: "loading" });
  const [tick, setTick] = useState(0);
  const current = useRef<AbortController | null>(null);

  useEffect(() => {
    if (path === null) return;
    const ctl = new AbortController();
    current.current = ctl;
    setState({ status: "loading" });
    request<T>(path, { signal: ctl.signal })
      .then((response) => {
        if (!ctl.signal.aborted) setState({ status: "success", response: response! });
      })
      .catch((error) => {
        if (!isAbortError(error) && !ctl.signal.aborted) setState({ status: "error", error });
      });
    return () => ctl.abort();
  }, [path, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return [state, reload];
}
