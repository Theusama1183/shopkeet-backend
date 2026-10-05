import { NextRequest, NextResponse } from "next/server";
import { API_BASE } from "@/lib/api";

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const res = await fetch(`${API_BASE}/auth/forgot-password`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => null);
    return NextResponse.json(data ?? {}, { status: res.status });
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the authentication service." } },
      { status: 503 }
    );
  }
}