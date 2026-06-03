// Auth context: holds the current user and exposes login/logout. On mount it
// attempts POST /auth/refresh (using the httpOnly cookie) to rehydrate a session
// that survives a page reload, since the access token itself lives only in
// memory. It also registers a listener so a background refresh (triggered by a
// 401 retry deeper in the app) keeps this state in sync.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import * as api from "../lib/api";
import type { User } from "../types";

export type AuthStatus = "loading" | "authenticated" | "anonymous";

interface AuthContextValue {
  user: User | null;
  status: AuthStatus;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [status, setStatus] = useState<AuthStatus>("loading");

  // Keep React state aligned with background token refreshes / clears.
  useEffect(() => {
    api.setAuthListener({
      onRefreshed: (u) => {
        setUser(u);
        setStatus("authenticated");
      },
      onCleared: () => {
        setUser(null);
        setStatus("anonymous");
      },
    });
    return () => api.setAuthListener({});
  }, []);

  // Rehydrate once on load.
  useEffect(() => {
    let cancelled = false;
    api
      .refresh()
      .then((res) => {
        if (cancelled) return;
        setUser(res.user);
        setStatus("authenticated");
      })
      .catch(() => {
        if (cancelled) return;
        setUser(null);
        setStatus("anonymous");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.login(email, password);
    setUser(res.user);
    setStatus("authenticated");
  }, []);

  const logout = useCallback(async () => {
    await api.logout();
    setUser(null);
    setStatus("anonymous");
  }, []);

  const value = useMemo(
    () => ({ user, status, login, logout }),
    [user, status, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within <AuthProvider>");
  return ctx;
}
