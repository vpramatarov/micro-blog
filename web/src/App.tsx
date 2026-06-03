import { Link, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth/AuthContext";
import { ProtectedRoute } from "./auth/ProtectedRoute";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { NotFound } from "./pages/NotFound";

function Nav() {
  const { status, user, logout } = useAuth();
  return (
    <nav className="nav">
      <Link to="/">Home</Link>
      <Link to="/dashboard">Dashboard</Link>
      <span className="nav-spacer" />
      {status === "authenticated" && user ? (
        <>
          <span className="nav-user">
            {user.username} ({user.role})
          </span>
          <button type="button" onClick={() => void logout()}>
            Log out
          </button>
        </>
      ) : status === "anonymous" ? (
        <Link to="/login">Log in</Link>
      ) : null}
    </nav>
  );
}

export function App() {
  return (
    <div className="app">
      <Nav />
      <main className="main">
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/login" element={<Login />} />
          <Route
            path="/dashboard"
            element={
              <ProtectedRoute>
                <Dashboard />
              </ProtectedRoute>
            }
          />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </main>
    </div>
  );
}
