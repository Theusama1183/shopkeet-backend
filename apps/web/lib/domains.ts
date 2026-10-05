/**
 * Client-safe origin helpers (no next/headers, no node:crypto — safe for
 * "use client" components and the auth forms).
 *
 * After login on auth.<ROOT_DOMAIN> the merchant lands on the unified admin at
 * admin.<ROOT_DOMAIN>; both live under the shared session cookie. In dev the
 * same trick works with `*.localhost` (Next serves every subdomain).
 */
export const ROOT_DOMAIN = process.env.NEXT_PUBLIC_ROOT_DOMAIN ?? "shopkeet.com"

/** Which Shopkeet subdomain is this browser on? null = bare domain / localhost. */
export function currentSubdomain(): "auth" | "admin" | null {
  if (typeof window === "undefined") return null
  const host = window.location.hostname
  if (host === "auth.localhost" || host.startsWith("auth.")) return "auth"
  if (host === "admin.localhost" || host.startsWith("admin.")) return "admin"
  return null
}

function originFor(subdomain: "auth" | "admin"): string {
  if (typeof window === "undefined") return `https://${subdomain}.${ROOT_DOMAIN}`
  const { protocol, hostname, port } = window.location
  const suffix = port ? `:${port}` : ""
  if (hostname === "localhost" || hostname === "127.0.0.1") {
    // Dev: the app runs on one port and proxies subdomains to the same app.
    return `${protocol}//${subdomain}.${hostname}${suffix}`
  }
  if (hostname.endsWith(".localhost")) {
    return `${protocol}//${subdomain}.${hostname.split(".").slice(-2).join(".")}${suffix}`
  }
  return `${protocol}//${subdomain}.${ROOT_DOMAIN}`
}

/** The admin host on the current origin — where the post-login bounce goes. */
export function adminOrigin(): string {
  return originFor("admin")
}

/** The auth host on the current origin — where login pages live. */
export function authOrigin(): string {
  return originFor("auth")
}