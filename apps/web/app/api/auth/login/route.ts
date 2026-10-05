import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { apiRequest, ApiError, type AuthStore } from "@/lib/api"
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  STORE_PICK_MAX_AGE_SECONDS,
  sessionCookieOptions,
  SESSION_MAX_AGE_SECONDS,
} from "@/lib/session"

export interface AuthSuccess {
  user: { id: string; email: string; role: string }
  stores: AuthStore[]
  token?: string
  store?: AuthStore
  store_pick_token?: string
  onboarding_completed?: boolean
}

interface LoginBody {
  email?: string
  password?: string
}

/**
 * Password in, session cookie out. The browser never sees a JWT.
 *
 * A merchant can own several stores, so this handler has two outcomes: one store
 * means a session cookie and straight to the dashboard; several means no session
 * yet — just the API's 10-minute store_pick ticket in an httpOnly cookie, and the
 * store list, so the browser can ask which store to open. The ticket carries no
 * tenant, so nothing is scoped until a store is actually chosen.
 */
export async function POST(request: Request) {
  let input: LoginBody
  try {
    input = await request.json()
  } catch {
    return NextResponse.json({ error: { code: "invalid_body", message: "Invalid request body." } }, { status: 400 })
  }

  let data: AuthSuccess
  try {
    data = await apiRequest<AuthSuccess>("/auth/login", { method: "POST", body: input })
  } catch (error) {
    if (error instanceof ApiError) {
      return NextResponse.json({ error: { code: error.code, message: error.message } }, { status: error.status })
    }
    return NextResponse.json({ error: { code: "unknown", message: "Something went wrong." } }, { status: 500 })
  }

  const jar = await cookies()
  const options = sessionCookieOptions()

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