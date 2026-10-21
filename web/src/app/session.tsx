import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { isAbortError, onUnauthorized, request } from "../api/client";
import type { CurrentUser } from "../api/types";

type Status = "loading" | "authenticated" | "anonymous";
type Session = { status: Status; user: CurrentUser | null; setUser: (u: CurrentUser | null) => void };

const SessionContext = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null);
  const [status, setStatus] = useState<Status>("loading");

  useEffect(() => {
    const ctl = new AbortController();
    request<CurrentUser>("/auth/me", { signal: ctl.signal })
      .then((res) => {
        setUser(res?.data ?? null);
        setStatus(res ? "authenticated" : "anonymous");
      })
      .catch((err) => {
        if (isAbortError(err)) return;
        setUser(null);
        setStatus("anonymous");
      });
    // A 401 anywhere drops the user; features keep unsent drafts in memory only.
    const off = onUnauthorized(() => {
      setUser(null);
      setStatus("anonymous");
    });
    return () => {
      ctl.abort();
      off();
    };
  }, []);

  const value = useMemo<Session>(
    () => ({
      status,
      user,
      setUser: (u) => {
        setUser(u);
        setStatus(u ? "authenticated" : "anonymous");
      },
    }),
    [status, user],
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): Session {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used inside SessionProvider");
  return ctx;
}
