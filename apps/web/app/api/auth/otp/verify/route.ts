import { NextRequest, NextResponse } from "next/server";
import { API_BASE } from "@/lib/api";
import { setSessionCookie } from "@/lib/session";

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const res = await fetch(`${API_BASE}/auth/otp/verify`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => null);

    if (res.ok && data?.token) {
      const response = NextResponse.json(data);
      setSessionCookie(response, data.token);
      // Add mode to help client decide where to redirect
      return NextResponse.json({ ...data, mode: data.mode ?? "session" });
    }

    return NextResponse.json(data ?? {}, { status: res.status });
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the authentication service." } },
      { status: 503 }
    );
  }
}