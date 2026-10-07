import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { apiRequest, ApiError, type AuthStore } from "@/lib/api"
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  STORE_PICK_MAX_AGE_SECONDS,
  sessionCookieOptions,
  SESSION_MAX_AGE_SECONDS,
  OTP_BYPASS_COOKIE,
} from "@/lib/session"

export interface AuthSuccess {
  user: { id: string; email: string; role: string }
  stores: AuthStore[]
  token?: string
  store?: AuthStore
  store_pick_token?: string
  onboarding_completed?: boolean
  /** Set instead of a token when the account passed the password but still owes an OTP. */
  mode?: "session" | "choose_store" | "otp_required"
  email?: string
  method?: string
}

interface LoginBody {
  email?: string
  password?: string
}

/**
 * Password in, session cookie out. The browser never sees a JWT.
 *
 * Three outcomes: the account's password passed and a single store exists →
 * session cookie and straight to the dashboard; several stores → no session
 * yet, just the API's 10-minute store_pick ticket in an httpOnly cookie plus
 * the store list; verification is enabled and the password merely passed →
 * {mode:"otp_required"} with neither cookie, because the emailed code is the
 * half of the login that actually proves the mailbox. The ticket carries no
 * tenant, so nothing is scoped until a store is actually chosen.
 *
 * A browser that already proved the mailbox carries an httpOnly otp_bypass
 * cookie (left behind by a successful /auth/otp/verify); it is forwarded here
 * as X-Otp-Bypass so the API can skip the code step for this account. Never
 * read from the client — it is only ever forwarded server-side.
 */
export async function POST(request: Request) {
  let input: LoginBody
  try {
    input = await request.json()
  } catch {
    return NextResponse.json({ error: { code: "invalid_body", message: "Invalid request body." } }, { status: 400 })
  }

  const jar = await cookies()
  const bypassToken = jar.get(OTP_BYPASS_COOKIE)?.value

  let data: AuthSuccess
  try {
    data = await apiRequest<AuthSuccess>("/auth/login", {
      method: "POST",
      body: input,
      headers: bypassToken ? { "x-otp-bypass": bypassToken } : undefined,
    })
  } catch (error) {
    if (error instanceof ApiError) {
      return NextResponse.json({ error: { code: error.code, message: error.message } }, { status: error.status })
    }
    return NextResponse.json({ error: { code: "unknown", message: "Something went wrong." } }, { status: 500 })
  }

  const options = sessionCookieOptions()

  // Password checked out but the OTP is still owed: no token exists yet, so
  // hand the step back verbatim. Drop any half-finished credentials first —
  // this browser is mid-login, not logged in.
  if (data.mode === "otp_required") {
    jar.set(SESSION_COOKIE, "", { ...options, maxAge: 0 })
    jar.set(STORE_PICK_COOKIE, "", { ...options, maxAge: 0 })
    return NextResponse.json({
      mode: "otp_required" as const,
      email: data.email,
      method: data.method ?? "email",
    })
  }

  if (data.token) {
    jar.set(SESSION_COOKIE, data.token, { ...options, maxAge: SESSION_MAX_AGE_SECONDS })
    // A new session invalidates any half-finished store selection.
    jar.delete(STORE_PICK_COOKIE)
    return NextResponse.json({
      mode: "session" as const,
      user: data.user,
      store: data.store,
      onboarding_completed: data.onboarding_completed ?? true,
    })
  }

  if (!data.store_pick_token) {
    return NextResponse.json(
      { error: { code: "no_store", message: "This account has no store yet." } },
      { status: 403 },
    )
  }

  jar.set(STORE_PICK_COOKIE, data.store_pick_token, { ...options, maxAge: STORE_PICK_MAX_AGE_SECONDS })
  // A fresh login must not leave a previous account's session behind — until a
  // store is chosen there is deliberately no session, and the pick is the only
  // credential this browser should be carrying.
  jar.set(SESSION_COOKIE, "", { ...options, maxAge: 0 })
  return NextResponse.json({
    mode: "choose_store" as const,
    user: data.user,
    stores: data.stores,
  })
}