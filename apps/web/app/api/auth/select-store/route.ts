import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { apiRequest, ApiError, type AuthStore } from "@/lib/api"
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  sessionCookieOptions,
  SESSION_MAX_AGE_SECONDS,
} from "@/lib/session"

interface SelectStoreResponse {
  token: string
  user: { id: string; role: string }
  store: AuthStore
  onboarding_completed: boolean
}

/**
 * Turns a chosen store into a session. Authorised by the store_pick ticket from
 * login, or by the merchant's own session when switching stores from the
 * dashboard — the Go API re-checks the membership either way, so neither token
 * can reach a store the account does not belong to.
 */
export async function POST(request: Request) {
  let input: { tenant_id?: string }
  try {
    input = await request.json()
  } catch {
    return NextResponse.json({ error: { code: "invalid_body", message: "Invalid request body." } }, { status: 400 })
  }
  if (!input.tenant_id) {
    return NextResponse.json({ error: { code: "invalid_body", message: "Pick a store first." } }, { status: 400 })
  }

  const jar = await cookies()
  const credential = jar.get(STORE_PICK_COOKIE)?.value ?? jar.get(SESSION_COOKIE)?.value
  if (!credential) {
    return NextResponse.json(
      { error: { code: "unauthorized", message: "Your login expired. Please log in again." } },
      { status: 401 },
    )
  }

  let data: SelectStoreResponse
  try {
    data = await apiRequest<SelectStoreResponse>(
      "/auth/select-store",
      { method: "POST", body: { tenant_id: input.tenant_id }, token: credential },
    )
  } catch (error) {
    if (error instanceof ApiError) {
      return NextResponse.json({ error: { code: error.code, message: error.message } }, { status: error.status })
    }
    return NextResponse.json({ error: { code: "unknown", message: "Something went wrong." } }, { status: 500 })
  }

  const options = sessionCookieOptions()
  jar.set(SESSION_COOKIE, data.token, { ...options, maxAge: SESSION_MAX_AGE_SECONDS })
  // The ticket has served its purpose; a stale one must not outlive the pick.
  jar.delete(STORE_PICK_COOKIE)

  return NextResponse.json({
    store: data.store,
    onboarding_completed: data.onboarding_completed,
  })
}