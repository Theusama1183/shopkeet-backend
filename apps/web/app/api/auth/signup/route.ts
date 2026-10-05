import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { apiRequest, ApiError, type AuthStore } from "@/lib/api"
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  sessionCookieOptions,
  SESSION_MAX_AGE_SECONDS,
} from "@/lib/session"

interface SignupBody {
  email?: string
  password?: string
}

interface SignupResponse {
  token: string
  user: { id: string; email: string; role: string }
  store: AuthStore
  onboarding_completed: boolean
}

/**
 * Account in, session cookie out — nothing else is asked here. The API creates
 * the merchant's first store provisionally and flags the session as needing the
 * one-time wizard, so the response tells the browser to open it rather than the
 * dashboard.
 */
export async function POST(request: Request) {
  let input: SignupBody
  try {
    input = await request.json()
  } catch {
    return NextResponse.json({ error: { code: "invalid_body", message: "Invalid request body." } }, { status: 400 })
  }

  let data: SignupResponse
  try {
    data = await apiRequest<SignupResponse>("/auth/signup", { method: "POST", body: input })
  } catch (error) {
    if (error instanceof ApiError) {
      return NextResponse.json({ error: { code: error.code, message: error.message } }, { status: error.status })
    }
    return NextResponse.json({ error: { code: "unknown", message: "Something went wrong." } }, { status: 500 })
  }

  const jar = await cookies()
  jar.set(SESSION_COOKIE, data.token, {
    ...sessionCookieOptions(),
    maxAge: SESSION_MAX_AGE_SECONDS,
  })
  jar.delete(STORE_PICK_COOKIE)
  return NextResponse.json({
    user: data.user,
    store: data.store,
    onboarding_completed: data.onboarding_completed,
  })
}