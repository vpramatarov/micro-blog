import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// API namespaces owned by the Go backend. In dev these are proxied to the Go
// server (default :8090) so the browser sees a single origin — which keeps the
// SameSite=Strict, Path=/auth refresh cookie working. "/" is intentionally NOT
// proxied: Vite owns it and serves the SPA.
const API_PREFIXES = [
  "/auth",
  "/api",
  "/admin",
  "/posts",
  "/categories",
  "/tags",
  "/p",
  "/s",
  "/uploads",
  "/openapi.yaml",
  "/openapi.json",
  "/docs",
];

// Dev-only proxy target — the Go API server. Defaults to the host port for
// `npm run dev` on the host; the Docker dev service overrides it to the Compose
// service name (`http://api:8080`) via VITE_API_PROXY_TARGET.
const target = process.env.VITE_API_PROXY_TARGET ?? "http://localhost:8090";

// Windows/WSL2 bind mounts don't deliver native filesystem events across the
// boundary, so the Docker dev service sets VITE_USE_POLLING=true to make HMR
// pick up host edits. Host-native dev leaves it off (event-based, lower CPU).
const usePolling = process.env.VITE_USE_POLLING === "true";

export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    host: true, // bind 0.0.0.0 so the published container port is reachable
    port: 5173,
    watch: usePolling ? { usePolling: true, interval: 300 } : undefined,
    // Anchor each prefix to a path-segment boundary so short prefixes like "/s"
    // (short links) don't swallow Vite's own module requests such as
    // "/src/main.tsx" — string keys match by prefix, so "/s" would proxy "/src/*"
    // to the API and the browser would receive HTML for a module script. Keys
    // starting with "^" are treated as RegExp by Vite's proxy.
    proxy: Object.fromEntries(
      API_PREFIXES.map((p) => [`^${p}(?:[/?]|$)`, { target, changeOrigin: false }]),
    ),
  },
});
