import { NextRequest, NextResponse } from "next/server";

import { API_BASE } from "@/lib/api";
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  STORE_PICK_MAX_AGE_SECONDS,
  sessionCookieOptions,
  setSessionCookie,
} from "@/lib/session";

interface ResetPayload {
  token?: string;
  store_pick_token?: string;
  stores?: unknown[];
  onboarding_completed?: boolean;
  error?: { code?: string; message?: string };
}

/**
 * A consumed reset link signs the merchant in — same shape as a completed
 * OTP verify: one store → session cookie, several → store_pick ticket only.
 * The cookie has to be set on the response that is returned; building a
 * response, seasoning it and then returning a different one (the original
 * bug here) drops Set-Cookie on the floor.
 */
export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const res = await fetch(`${API_BASE}/auth/reset-password`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = (await res.json().catch(() => null)) as ResetPayload | null;

    if (!res.ok || !data) {
      return NextResponse.json(data ?? {}, { status: res.status });
    }

    const options = sessionCookieOptions();

    if (data.token) {
      const response = NextResponse.json({ ...data, mode: "session" });
      setSessionCookie(response, data.token);
      response.cookies.set(STORE_PICK_COOKIE, "", { ...options, maxAge: 0 });
      return response;
    }

    if (data.store_pick_token) {
      const response = NextResponse.json({ ...data, mode: "choose_store" });
      response.cookies.set(STORE_PICK_COOKIE, data.store_pick_token, {
        ...options,
        maxAge: STORE_PICK_MAX_AGE_SECONDS,
      });
      response.cookies.set(SESSION_COOKIE, "", { ...options, maxAge: 0 });
      return response;
    }

    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the authentication service." } },
      { status: 503 }
    );
  }
}
