import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

const base: string = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";

// Which cached queries each server event makes stale. Events carry no data:
// REST stays the source of truth.
const STALE: Record<string, string[]> = {
  "execution.updated": ["executions", "history", "migrations", "head"],
  "request.updated": ["requests", "migrations"],
  "migration.updated": ["migrations", "history"],
  "head.updated": ["head", "history", "schema"],
};

const ACTIVE_POLL_MS = 5_000;

/**
 * Keeps the queries of one database fresh while its pages are open. While
 * the stream is down (proxy, sleeping mobile tab) active executions are
 * polled every 5 seconds; on reconnect everything is refetched, since
 * missed events are not replayed.
 */
export function useDatabaseEvents(databaseId: string | undefined): void {
  const queryClient = useQueryClient();
  const [connected, setConnected] = useState(true);

  useEffect(() => {
    if (!databaseId) return;
    const source = new EventSource(`${base}/databases/${encodeURIComponent(databaseId)}/events`, { withCredentials: true });
    const refetchAll = () => queryClient.invalidateQueries({ predicate: (q) => q.queryKey[1] === databaseId });
    let wasDown = false;
    source.onopen = () => {
      setConnected(true);
      if (wasDown) void refetchAll();
      wasDown = false;
    };
    source.onerror = () => {
      wasDown = true;
      setConnected(false); // EventSource reconnects by itself (retry: 3000)
    };
    for (const [type, features] of Object.entries(STALE)) {
      source.addEventListener(type, () => {
        for (const feature of features) void queryClient.invalidateQueries({ queryKey: [feature, databaseId] });
      });
    }
    return () => source.close();
  }, [databaseId, queryClient]);

  useEffect(() => {
    if (!databaseId || connected) return;
    const timer = setInterval(() => {
      void queryClient.invalidateQueries({ queryKey: ["executions", databaseId] });
    }, ACTIVE_POLL_MS);
    return () => clearInterval(timer);
  }, [databaseId, connected, queryClient]);
}
