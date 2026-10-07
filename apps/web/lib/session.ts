import { createHmac, timingSafeEqual } from "node:crypto"

import { cookies } from "next/headers"
import { NextResponse } from "next/server"

export const SESSION_COOKIE = "shopkeet_session"
/**
 * Carries the API's short-lived store_pick ticket between a successful login
 * and the merchant choosing a store. It stands in for the password for those few
 * minutes, so it lives in an httpOnly cookie like the session itself, and shares
 * the session's domain — the store list and the pick both run on the admin host
 * while the ticket was minted on the auth host, so it has to be readable there.
 * The browser never sees it; the ticket carries no tenant, only the account.
 */
export const STORE_PICK_COOKIE = "shopkeet_store_pick"
/**
 * "Trusted device" ticket handed back by a successful OTP verification. The web
 * app stores it as its own httpOnly cookie and forwards it on login, so a
 * browser that already proved the mailbox skips the code on later logins. The
 * API binds the ticket to the account's current password hash, so a password
 * change kills it regardless of expiry. Unlike the session cookie it is NOT
 * cleared on logout — being a trusted device survives a logout, which is what
 * makes repeat logins OTP-free.
 */
export const OTP_BYPASS_COOKIE = "otp_bypass"
export const ROOT_DOMAIN = process.env.NEXT_PUBLIC_ROOT_DOMAIN ?? "shopkeet.com"

const JWT_SECRET = process.env.JWT_SECRET ?? ""

/**
 * Cookie domain shared across auth./admin. subdomains, per the 13-spec: a login
 * set on auth.shopkeet.com must be readable on admin.shopkeet.com. In development
 * the same trick works with `*.localhost`. Scope customer sessions to a single
 * tenant subdomain instead — never pass a shared domain for those.
 */
export const SESSION_COOKIE_DOMAIN =
  process.env.SESSION_COOKIE_DOMAIN ??
  (process.env.NODE_ENV === "production" ? `.${ROOT_DOMAIN}` : ".localhost")

export interface TenantSession {
  tenant_id: string
  user_id: string
  role: string
  scope: string
  /** The account identity, so a merchant can list and switch stores. */
  account_id?: string
  /**
   * false only for a session minted by signup or before the one-time store
   * wizard ran. Absent on older tokens, which counts as onboarded.
   */
  onboarding_completed?: boolean
  exp: number
  [key: string]: unknown
}

export const SESSION_MAX_AGE_SECONDS = 60 * 60 * 24 // the API mints 24h merchant tokens

/** Matches the API's 10-minute store_pick ticket. */
export const STORE_PICK_MAX_AGE_SECONDS = 10 * 60

/** Matches the API's trusted-device ticket lifetime. */
export const OTP_BYPASS_MAX_AGE_SECONDS = 90 * 24 * 60 * 60

function b64urlDecode(input: string): string {
  return Buffer.from(input, "base64url").toString("utf8")
}

function verifySignature(token: string): boolean {
  const parts = token.split(".")
  if (parts.length !== 3) return false
  const signature = Buffer.from(parts[2], "base64url")
  const expected = createHmac("sha256", JWT_SECRET).update(`${parts[0]}.${parts[1]}`).digest()
  if (signature.length !== expected.length) return false
  return timingSafeEqual(signature, expected)
}

/**
 * Verifies the JWT signature (HS256, shared with the Go API) and returns the
 * claims — or null for anything forged/expired/non-merchant. The admin shell
 * must never trust a decoded-but-unverified token; if JWT_SECRET isn't
 * configured this deliberately refuses every session.
 */
export function decodeSession(token: string): TenantSession | null {
  if (!JWT_SECRET) return null
  if (!verifySignature(token)) return null
  try {
    const header = JSON.parse(b64urlDecode(token.split(".")[0])) as { alg?: string }
    if (header.alg !== "HS256") return null
    const claims = JSON.parse(b64urlDecode(token.split(".")[1])) as TenantSession
    if (claims.scope !== "merchant") return null
    if (typeof claims.exp !== "number" || claims.exp * 1000 < Date.now()) return null
    return claims
  } catch {
    return null
  }
}

/**
 * Verifies a store_pick ticket the same way as decodeSession but accepts the
 * ticket's own scope instead of requiring a merchant session. The ticket is
 * only ever handed to the Go API as a Bearer token — it is never treated as a
 * session here, and carries no tenant, so it cannot be used to read a store.
 */
export function decodeStorePickTicket(token: string): { account_id: string } | null {
  if (!JWT_SECRET || !token) return null
  if (!verifySignature(token)) return null
  try {
    const header = JSON.parse(b64urlDecode(token.split(".")[0])) as { alg?: string }
    if (header.alg !== "HS256") return null
    const claims = JSON.parse(b64urlDecode(token.split(".")[1])) as TenantSession
    if (claims.scope !== "store_pick") return null
    if (typeof claims.exp !== "number" || claims.exp * 1000 < Date.now()) return null
    if (typeof claims.account_id !== "string" || claims.account_id === "") return null
    return { account_id: claims.account_id }
  } catch {
    return null
  }
}

export interface VerifiedSession {
  token: string
  claims: TenantSession
}

/**
 * Reads and validates the merchant session cookie, handing server components
 * both the verified claims and the raw JWT (the latter is the Bearer token for
 * admin API calls). Server components only.
 */
export async function getSession(): Promise<VerifiedSession | null> {
  const store = await cookies()
  const token = store.get(SESSION_COOKIE)?.value
  if (!token) return null
  const claims = decodeSession(token)
  if (!claims) return null
  return { token, claims }
}

export function sessionCookieOptions(): {
  httpOnly: boolean
  secure: boolean
  sameSite: "lax"
  path: string
  domain?: string
} {
  return {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    domain: SESSION_COOKIE_DOMAIN,
  }
}

/**
 * Sets the session cookie on a response. Server-side only (API routes).
 */
export function setSessionCookie(
  response: NextResponse,
  token: string,
  maxAge = SESSION_MAX_AGE_SECONDS
): void {
  response.cookies.set(SESSION_COOKIE, token, {
    ...sessionCookieOptions(),
    maxAge,
  })
}

/**
 * Sets the trusted-device cookie on a response. Server-side only (API routes).
 * Deliberately the same domain/options as the session cookie — the ticket has
 * to be readable on both auth.* and admin.* so the login route (auth host) can
 * forward it no matter where the browser wandered.
 */
export function setOTPBypassCookie(response: NextResponse, token: string): void {
  response.cookies.set(OTP_BYPASS_COOKIE, token, {
    ...sessionCookieOptions(),
    maxAge: OTP_BYPASS_MAX_AGE_SECONDS,
  })
}

/**
 * Server-side sibling of the client adminOrigin(): derives the auth origin from
 * the current request's Host header so server actions can bounce the browser
 * across the subdomain boundary (admin.shopkeet.com → auth.shopkeet.com; dev:
 *  admin.localhost:PORT → auth.localhost:PORT).
 */
export function authOriginFromHost(host: string): string {
  const [hostname, ...portParts] = host.split(":")
  const port = portParts.length > 0 ? `:${portParts.join(":")}` : ""
  if (hostname === "localhost" || hostname === "127.0.0.1" || hostname.endsWith(".localhost")) {
    return `http://auth.localhost${port}`
  }
  return `https://auth.${ROOT_DOMAIN}`
}