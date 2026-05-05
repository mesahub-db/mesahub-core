/**
 * Proxy helper for calling the Go backend (internal, never exposed to browsers).
 *
 * - goFetchAdmin  – for admin-only endpoints; always uses the ADMIN_TOKEN Bearer.
 * - goFetchDb     – for per-DB endpoints; uses ADMIN_TOKEN when the admin session
 *                   header is present, otherwise forwards the caller's Bearer token
 *                   (e.g. a scoped shs_ API key from an external client).
 */

const GO_API_URL = (process.env.GO_API_URL ?? "http://localhost:3000").replace(/\/$/, "");
const ADMIN_TOKEN = process.env.ADMIN_TOKEN ?? "";
/** Default timeout for all Go backend calls — prevents tab freeze when Go is slow/down. */
const GO_FETCH_TIMEOUT_MS = 10_000;

/** Call a Go API endpoint as the admin (ADMIN_TOKEN Bearer). */
export function goFetchAdmin(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${GO_API_URL}${path}`, {
    signal: AbortSignal.timeout(GO_FETCH_TIMEOUT_MS),
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
      Authorization: `Bearer ${ADMIN_TOKEN}`,
    },
  });
}

/**
 * Call a Go per-DB endpoint.
 * If the incoming request has already been verified as an admin session
 * (x-mesahub-admin: 1 stamped by the Next.js middleware), use the admin
 * token. Otherwise forward the caller's own Authorization header so Go can
 * validate the scoped API key.
 */
export function goFetchDb(
  path: string,
  incomingReq: Request,
  init?: RequestInit
): Promise<Response> {
  const isAdmin = incomingReq.headers.get("x-mesahub-admin") === "1";
  const authHeader = isAdmin
    ? `Bearer ${ADMIN_TOKEN}`
    : (incomingReq.headers.get("authorization") ?? "");

  const headers: Record<string, string> = {
    ...(init?.headers as Record<string, string> | undefined),
  };
  if (authHeader) headers["Authorization"] = authHeader;

  return fetch(`${GO_API_URL}${path}`, {
    signal: AbortSignal.timeout(GO_FETCH_TIMEOUT_MS),
    ...init,
    headers,
  });
}
