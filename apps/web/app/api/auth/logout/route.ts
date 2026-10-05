import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { SESSION_COOKIE, STORE_PICK_COOKIE, sessionCookieOptions } from "@/lib/session"

export async function POST() {
  const store = await cookies()
  // Both credentials go: a half-finished store pick must not outlive the logout
  // that was meant to end the session.
  store.set(SESSION_COOKIE, "", { ...sessionCookieOptions(), maxAge: 0 })
  store.set(STORE_PICK_COOKIE, "", { ...sessionCookieOptions(), maxAge: 0 })
  return NextResponse.json({ ok: true })
}