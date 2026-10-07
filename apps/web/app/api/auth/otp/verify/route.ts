import { NextRequest, NextResponse } from "next/server";

import { API_BASE } from "@/lib/api";
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  STORE_PICK_MAX_AGE_SECONDS,
  sessionCookieOptions,
  setSessionCookie,
  setOTPBypassCookie,
} from "@/lib/session";

interface VerifyPayload {
  token?: string;
  store_pick_token?: string;
  stores?: unknown[];
  onboarding_completed?: boolean;
  /**
   * "Remember this browser" ticket from a correct code: stored as its own
   * httpOnly cookie so the next login (see /api/auth/login) can skip the OTP
   * step for this account. Absent only when minting failed server-side.
   */
  device_token?: string;
  error?: { code?: string; message?: string };
}

/**
 * Code in, session out — the twin of /api/auth/login, one factor later. One
 * store means the tenant JWT becomes an httpOnly session cookie; several means
 * only the store_pick ticket is stored (same cookie login uses), because the
 * API deliberately refuses to pick a store on the merchant's behalf. The
 * returned mode is what the OTP page navigates by, and it must be set on the
 * SAME response that carries the cookie — mutating a throwaway response and
 * returning another one silently drops the Set-Cookie header.
 */
export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const res = await fetch(`${API_BASE}/auth/otp/verify`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = (await res.json().catch(() => null)) as VerifyPayload | null;

    if (!res.ok || !data) {
      return NextResponse.json(data ?? {}, { status: res.status });
    }

    const options = sessionCookieOptions();

    if (data.token) {
      const response = NextResponse.json({ ...data, mode: "session" });
      setSessionCookie(response, data.token);
      // A completed verification replaces any half-finished store selection.
      response.cookies.set(STORE_PICK_COOKIE, "", { ...options, maxAge: 0 });
      if (data.device_token) setOTPBypassCookie(response, data.device_token);
      return response;
    }

    if (data.store_pick_token) {
      const response = NextResponse.json({ ...data, mode: "choose_store" });
      response.cookies.set(STORE_PICK_COOKIE, data.store_pick_token, {
        ...options,
        maxAge: STORE_PICK_MAX_AGE_SECONDS,
      });
      response.cookies.set(SESSION_COOKIE, "", { ...options, maxAge: 0 });
      if (data.device_token) setOTPBypassCookie(response, data.device_token);
      return response;
    }

    // Neither: an account with no store yet (403 from the API) — pass it through.
    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the authentication service." } },
      { status: 503 }
    );
  }
}
