import { SessionData, sessionOptions } from "@/lib/session";
import { FILES_ENABLED } from "@/lib/features";
import { getIronSession } from "iron-session";
import { NextRequest, NextResponse } from "next/server";

const PUBLIC_PREFIXES = ["/login", "/api/health", "/api/auth", "/api/openapi"];
const API_VERSION_PREFIX = "/api/v1";
const CORS_ALLOWED_ORIGINS = (process.env.CORS_ALLOWED_ORIGINS ?? "")
  .split(",")
  .map((value) => value.trim())
  .filter(Boolean);

// These routes enforce their own resource auth (scoped API key)
const RESOURCE_SCOPED_PATTERN = /^\/api\/(?:db|buckets)(?:\/[^/]+(?:\/.*)?)?$/;
const DB_FILES_PAGE_PATTERN = /^\/db\/[^/]+\/files(?:\/.*)?$/;
const DB_FILES_API_PATTERN = /^\/api\/db\/[^/]+\/(?:files|tokens\/files)(?:\/.*)?$/;
const BUCKET_PAGE_PATTERN = /^\/buckets(?:\/.*)?$/;
const BUCKET_API_PATTERN = /^\/api\/buckets(?:\/.*)?$/;

// Public file shortlink pattern: /{dbName}/file/{fileId}
const FILE_SHORTLINK_PATTERN = /^\/[^/]+\/file\/[^/]+$/;

// Trusted internal header stamped by middleware after session verification.
// Stripped from all incoming requests to prevent external forgery.
export const ADMIN_SESSION_HEADER = "x-mesahub-admin";

function resolveAllowedOrigin(req: NextRequest): string | null {
  const origin = req.headers.get("origin");
  if (!origin) return null;
  if (CORS_ALLOWED_ORIGINS.includes("*")) return "*";
  if (CORS_ALLOWED_ORIGINS.includes(origin)) return origin;
  return null;
}

function applyCorsHeaders(req: NextRequest, res: NextResponse): NextResponse {
  const allowedOrigin = resolveAllowedOrigin(req);
  if (!allowedOrigin) return res;

  res.headers.set("Vary", "Origin");
  res.headers.set("Access-Control-Allow-Origin", allowedOrigin);
  res.headers.set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS,HEAD");
  res.headers.set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With");
  if (allowedOrigin !== "*") {
    res.headers.set("Access-Control-Allow-Credentials", "true");
  }

  return res;
}

function stripApiVersionPrefix(pathname: string): string {
  if (pathname === API_VERSION_PREFIX) return "/api";
  if (pathname.startsWith(`${API_VERSION_PREFIX}/`)) {
    return pathname.replace(API_VERSION_PREFIX, "/api");
  }
  return pathname;
}

export async function middleware(req: NextRequest) {
  const rewrittenPathname = stripApiVersionPrefix(req.nextUrl.pathname);
  const shouldRewrite = rewrittenPathname !== req.nextUrl.pathname;

  // Strip any externally supplied admin-session header to prevent forgery
  const forwarded = new Headers(req.headers);
  forwarded.delete(ADMIN_SESSION_HEADER);

  if (!FILES_ENABLED && (DB_FILES_PAGE_PATTERN.test(rewrittenPathname) || DB_FILES_API_PATTERN.test(rewrittenPathname) || FILE_SHORTLINK_PATTERN.test(rewrittenPathname))) {
    if (rewrittenPathname.startsWith("/api/")) {
      return NextResponse.json({ error: "Files are temporarily disabled" }, { status: 404 });
    }
    return new NextResponse(null, { status: 404 });
  }

  if (!FILES_ENABLED && (BUCKET_PAGE_PATTERN.test(rewrittenPathname) || BUCKET_API_PATTERN.test(rewrittenPathname))) {
    if (rewrittenPathname.startsWith("/api/")) {
      return NextResponse.json({ error: "Buckets are temporarily disabled" }, { status: 404 });
    }
    return new NextResponse(null, { status: 404 });
  }

  if ((rewrittenPathname === "/api" || rewrittenPathname.startsWith("/api/")) && req.method === "OPTIONS") {
    return applyCorsHeaders(req, new NextResponse(null, { status: 204 }));
  }

  // Handle OPTIONS for file shortlinks
  if (FILE_SHORTLINK_PATTERN.test(rewrittenPathname) && req.method === "OPTIONS") {
    return applyCorsHeaders(req, new NextResponse(null, { status: 204 }));
  }

  function continueResponse(): NextResponse {
    if (shouldRewrite) {
      const rewriteUrl = req.nextUrl.clone();
      rewriteUrl.pathname = rewrittenPathname;
      return NextResponse.rewrite(rewriteUrl, { request: { headers: forwarded } });
    }
    return NextResponse.next({ request: { headers: forwarded } });
  }

  const isPublic = PUBLIC_PREFIXES.some((prefix) => rewrittenPathname.startsWith(prefix));

  if (isPublic) {
    return applyCorsHeaders(req, continueResponse());
  }

  // Public file shortlinks: allow without session, apply CORS
  if (FILE_SHORTLINK_PATTERN.test(rewrittenPathname)) {
    return applyCorsHeaders(req, continueResponse());
  }

  // DB-scoped routes: if session is valid, stamp the trusted header so route
  // handlers know this is an authenticated admin browser request.
  // Skip the session crypto entirely when a Bearer token is present — the
  // route handler will validate it directly.
  if (RESOURCE_SCOPED_PATTERN.test(rewrittenPathname)) {
    if (!forwarded.has("authorization")) {
      const tempRes = NextResponse.next();
      const session = await getIronSession<SessionData>(req, tempRes, sessionOptions);
      if (session.isLoggedIn) {
        forwarded.set(ADMIN_SESSION_HEADER, "1");
      }
    }
    return applyCorsHeaders(req, continueResponse());
  }

  // All other routes: require a valid browser session (session cookie only)

  // Browser: check session cookie
  const res = continueResponse();
  const session = await getIronSession<SessionData>(req, res, sessionOptions);

  if (!session.isLoggedIn) {
    if (rewrittenPathname.startsWith("/api/")) {
      return applyCorsHeaders(req, NextResponse.json({ error: "Unauthorized" }, { status: 401 }));
    }
    const loginUrl = new URL("/login", req.url);
    return NextResponse.redirect(loginUrl);
  }

  return applyCorsHeaders(req, res);
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|fonts|favicon.ico|icon).*)"],
};
