import { NextResponse } from "next/server"
import { cookies } from "next/headers"

import { apiRequest, ApiError, type AuthStore } from "@/lib/api"
import { STORE_PICK_COOKIE } from "@/lib/session"

interface StoreListResponse {
  stores: AuthStore[]
}

/**
 * Lists the stores an account can open. Authorised by the store_pick ticket the
 * login handler stored in an httpOnly cookie — the browser passes no token of its
 * own, so the list stays unreadable to any script on the page.
 */
export async function GET() {
  const jar = await cookies()
  const ticket = jar.get(STORE_PICK_COOKIE)?.value
  if (!ticket) {
    return NextResponse.json({ stores: [] })
  }

  let data: StoreListResponse
  try {
    data = await apiRequest<StoreListResponse>("/auth/stores", { token: ticket })
  } catch (error) {
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
      // Ticket expired or revoked — drop it so the browser goes back to login
      // rather than looping on a dead selection.
      jar.delete(STORE_PICK_COOKIE)
      return NextResponse.json({ stores: [] })
    }
    if (error instanceof ApiError) {
      return NextResponse.json({ error: { code: error.code, message: error.message } }, { status: error.status })
    }
    return NextResponse.json({ error: { code: "unknown", message: "Something went wrong." } }, { status: 500 })
  }

  return NextResponse.json({ stores: data.stores })
}