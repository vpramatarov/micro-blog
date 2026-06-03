// Gates a route on authentication. While the initial refresh is resolving it
// renders a lightweight loading state; once resolved, anonymous users are sent
// to /login (preserving where they were headed).

import { Navigate, useLocation } from "react-router-dom";
import type { ReactNode } from "react";
import { useAuth } from "./AuthContext";

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const { status } = useAuth();
  const location = useLocation();

  if (status === "loading") {
    return <p style={{ padding: "2rem" }}>Loading…</p>;
  }
  if (status === "anonymous") {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}
