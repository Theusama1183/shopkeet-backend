import { NextRequest, NextResponse } from "next/server"

const ROOT_DOMAIN = process.env.NEXT_PUBLIC_ROOT_DOMAIN ?? "shopkeet.com"

/**
 * Multi-domain routing (13-nextjs-domain-routing-spec.md). One Next.js app
 * serves auth./admin./{tenant}. — route groups can't be rewrite destinations
 * (invisible to the URL), so real top-level folders rewrite here.
 *
 * Canonical public URLs are the subdomain forms: auth.<root>/login,
 * admin.<root>/orders, {tenant}.<root>/. The /auth and /admin path prefixes
 * are implementation detail and never appear in the address bar.
 *
 * Next.js 16 calls this file `proxy.ts` (was middleware.ts on 15 and earlier).
 */
export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl
  const hostWithPort = request.headers.get("host") ?? ""
  const host = hostWithPort.split(":")[0]

  const isAuthHost = host === `auth.${ROOT_DOMAIN}` || host === "auth.localhost"
  const isAdminHost = host === `admin.${ROOT_DOMAIN}` || host === "admin.localhost"
  // Bare localhost/127.0.0.1 = the app's own pages (marketing / dev gallery), and
  // the apex/www domain in production.
  const isRootHost =
    host === "localhost" || host === "127.0.0.1" || host === ROOT_DOMAIN || host === `www.${ROOT_DOMAIN}`

  // API routes are host-agnostic — the browser posts to /api/auth/* from every
  // subdomain and must reach the handler unmodified, never /auth/api/auth/*.
  if (pathname.startsWith("/api/") || pathname.startsWith("/_next") || pathname.startsWith("/dev")) {
    return NextResponse.next()
  }

  if (isAuthHost) {
    // The auth app has no index — / goes to the login screen.
    if (pathname === "/") return NextResponse.redirect(selfUrl(request, "/login"))
    // /auth/* is the path prefix this app happens to use, never the URL a
    // merchant should see — bounce the old form onto the canonical path.
    if (pathname === "/auth" || pathname.startsWith("/auth/")) {
      return NextResponse.redirect(selfUrl(request, stripPrefix(pathname, "/auth") || "/login"), 308)
    }
    return NextResponse.rewrite(new URL(`/auth${stripPrefix(pathname, "/auth")}`, request.url))
  }

  if (isAdminHost) {
    // A stray auth link on the admin host must cross back to the auth origin
    // instead of being rewritten into /admin/auth/* (which 404s).
    if (pathname === "/auth" || pathname.startsWith("/auth/") || pathname === "/login" || pathname === "/signup") {
      return NextResponse.redirect(authUrl(request, pathname === "/login" || pathname === "/signup" ? pathname : "/login"))
    }
    if (pathname === "/") return NextResponse.rewrite(new URL("/admin", request.url))
    return NextResponse.rewrite(new URL(`/admin${stripPrefix(pathname, "/admin")}`, request.url))
  }

  if (isRootHost) {
    // Keep one canonical auth URL: the apex host bounces /auth/* and bare
    // /login,/signup up to auth.<root> so the address bar always shows it.
    const authPath = pathname === "/auth" || pathname.startsWith("/auth/") ? stripPrefix(pathname, "/auth") || "/login" : pathname
    if (authPath === "/login" || authPath === "/signup" || authPath === "/otp" || authPath === "/reset-password") {
      return NextResponse.redirect(authUrl(request, authPath))
    }
    return NextResponse.next()
  }

  // Everything else is a tenant storefront subdomain. Custom domains need the
  // GET /tenants/by-domain lookup noted in the spec — not implemented here.
  const tenantSlug = host.replace(`.${ROOT_DOMAIN}`, "")
  return NextResponse.rewrite(new URL(`/storefront/${tenantSlug}${pathname}`, request.url))
}

/** Drops a leading `prefix` so `/auth/login` and `/login` resolve identically. */
function stripPrefix(pathname: string, prefix: string): string {
  if (pathname === prefix) return "/"
  if (pathname.startsWith(`${prefix}/`)) return pathname.slice(prefix.length)
  return pathname
}

/**
 * Absolute URL on the *same* host as the request, for redirects that must not
 * change subdomain. NextRequest.url is resolved from the connection rather than
 * the Host header, so a subdomain request would otherwise be sent back to the
 * bare host — which is exactly what multi-domain routing must never do.
 */
function selfUrl(request: NextRequest, pathname: string): URL {
  const url = new URL(pathname, request.url)
  const hostWithPort = request.headers.get("host")
  if (hostWithPort) url.host = hostWithPort
  return url
}

/**
 * Absolute URL on the auth origin for this request. Keeps the dev port so
 * auth.localhost:3000 works without Next's *.localhost redirect, and falls back
 * to the request host when ROOT_DOMAIN isn't a real domain (localhost dev).
 */
function authUrl(request: NextRequest, pathname: string): URL {
  const hostWithPort = request.headers.get("host") ?? ""
  const host = hostWithPort.split(":")[0]
  const port = hostWithPort.includes(":") ? `:${hostWithPort.split(":")[1]}` : ""
  const protocol = request.nextUrl.protocol
  if (host.endsWith(".localhost") || host === "localhost" || host === "127.0.0.1") {
    return new URL(`${protocol}//auth.localhost${port}${pathname}`)
  }
  return new URL(`${protocol}//auth.${ROOT_DOMAIN}${pathname}`)
}

export const config = {
  matcher: ["/((?!_next|_static|_vercel|.*\\..*).*)"],
}