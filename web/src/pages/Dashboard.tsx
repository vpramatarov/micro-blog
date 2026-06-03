// Placeholder protected page. Later plans build the real role-aware management
// UI here; for now it just proves the auth gate + session work.

import { useAuth } from "../auth/AuthContext";

export function Dashboard() {
  const { user } = useAuth();
  return (
    <section>
      <h1>Dashboard</h1>
      {user && (
        <p>
          Signed in as <strong>{user.username}</strong> ({user.email}) — role{" "}
          <strong>{user.role}</strong>.
        </p>
      )}
      <p>Feature pages land here in upcoming work.</p>
    </section>
  );
}
