// Typed fetch client for the micro-blog API.
//
// - The access token lives in memory only (never localStorage) and is attached
//   as `Authorization: Bearer`. The refresh token is an httpOnly cookie the
//   browser sends automatically (credentials: "include").
// - On a 401, an authed request transparently calls POST /auth/refresh once and
//   retries. If refresh fails, the token is cleared and the registered listener
//   is notified so the auth context can drop to anonymous.
// - All URLs are same-origin relative: works embedded in the Go binary (prod)
//   and through the Vite dev proxy (dev).

import type { ApiErrorBody, AuthResponse, Page, Post, User } from "../types";

let accessToken: string | null = null;

export function getAccessToken(): string | null {
  return accessToken;
}

// Listener lets the React auth context stay in sync when a background refresh
// (triggered by a 401 retry) succeeds or fails outside an explicit login call.
interface AuthListener {
  onRefreshed?: (user: User) => void;
  onCleared?: () => void;
}
let listener: AuthListener = {};
export function setAuthListener(l: AuthListener): void {
  listener = l;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields?: Record<string, string>;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || body.error || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body.error;
    this.fields = body.fields;
  }
}

interface RequestOpts {
  method?: string;
  body?: unknown; // JSON-encoded when present
  auth?: boolean; // attach bearer token (default true)
  retryOn401?: boolean; // attempt refresh+retry on 401 (default true)
}

async function rawRequest(path: string, opts: RequestOpts): Promise<Response> {
  const headers: Record<string, string> = {};
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (opts.auth !== false && accessToken) {
    headers["Authorization"] = `Bearer ${accessToken}`;
  }
  return fetch(path, {
    method: opts.method ?? "GET",
    headers,
    credentials: "include",
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody = { error: "unknown" };
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    /* non-JSON error body (e.g. proxy/5xx) — keep the default */
  }
  return new ApiError(res.status, body);
}

// Coalesce concurrent refreshes so a burst of 401s triggers one /auth/refresh.
let refreshInFlight: Promise<boolean> | null = null;

function tryRefresh(): Promise<boolean> {
  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      try {
        const res = await rawRequest("/auth/refresh", { method: "POST", auth: false });
        if (!res.ok) return false;
        const data = (await res.json()) as AuthResponse;
        accessToken = data.access_token;
        listener.onRefreshed?.(data.user);
        return true;
      } catch {
        return false;
      } finally {
        refreshInFlight = null;
      }
    })();
    // Clear token + notify on definitive failure, after the shared promise settles.
    refreshInFlight.then((ok) => {
      if (!ok) {
        accessToken = null;
        listener.onCleared?.();
      }
    });
  }
  return refreshInFlight;
}

async function request<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  let res = await rawRequest(path, opts);

  if (res.status === 401 && opts.auth !== false && opts.retryOn401 !== false) {
    if (await tryRefresh()) {
      res = await rawRequest(path, opts);
    }
  }

  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// ---- Endpoint methods -------------------------------------------------------

export async function login(email: string, password: string): Promise<AuthResponse> {
  const data = await request<AuthResponse>("/auth/login", {
    method: "POST",
    body: { email, password },
    auth: false,
    retryOn401: false,
  });
  accessToken = data.access_token;
  return data;
}

// refresh rehydrates the session on app load using the httpOnly cookie. Throws
// (401) when there is no valid refresh cookie — caller treats that as anonymous.
export async function refresh(): Promise<AuthResponse> {
  const data = await request<AuthResponse>("/auth/refresh", {
    method: "POST",
    auth: false,
    retryOn401: false,
  });
  accessToken = data.access_token;
  return data;
}

export async function logout(): Promise<void> {
  try {
    await request<void>("/auth/logout", { method: "POST", retryOn401: false });
  } finally {
    accessToken = null;
  }
}

export async function getMe(): Promise<User> {
  return request<User>("/api/me");
}

export async function listPosts(page = 1, perPage = 20): Promise<Page<Post>> {
  return request<Page<Post>>(`/posts?page=${page}&per_page=${perPage}`, { auth: false });
}

// getPost - get published post by slug. Throws ApiError with status 404 for not found entry.
export async function getPost(slug: string): Promise<Post> {
  return request<Post>(`/posts/${encodeURIComponent(slug)}`, {auth: false})
}