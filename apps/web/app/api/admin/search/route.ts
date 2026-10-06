import { NextResponse } from "next/server";

import { ADMIN_API_BASE } from "@/lib/admin-api";
import { getSession } from "@/lib/session";

/**
 * Proxy for the admin record search (GET /search). The palette in the browser
 * cannot attach the merchant JWT — it lives in an httpOnly cookie — so the
 * command-menu fetch hits this route and this handler forwards the cookie's
 * token to the Go API, returning JSON the palette can render directly.
 *
 * Unlike the PDF proxy this returns 401/400 JSON rather than a redirect: the
 * caller is an XHR keyed to Ctrl+K, so a bounce loses the search the merchant
 * just typed.
 */
export async function GET(req: Request) {
  const session = await getSession();

  if (!session) {
    return NextResponse.json(
      { error: { code: "unauthorized", message: "Sign in to search your store." } },
      { status: 401 }
    );
  }

  const q = new URL(req.url).searchParams.get("q") ?? "";
  if (!q.trim()) {
    return NextResponse.json(
      { error: { code: "missing_q", message: "q query parameter is required." } },
      { status: 400 }
    );
  }

  let upstream: Response;
  try {
    upstream = await fetch(`${ADMIN_API_BASE}/search?q=${encodeURIComponent(q)}`, {
      headers: { authorization: `Bearer ${session.token}` },
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the Shopkeet API." } },
      { status: 502 }
    );
  }

  if (upstream.status === 401 || upstream.status === 403) {
    return NextResponse.json(
      { error: { code: "unauthorized", message: "Your session expired. Sign in again." } },
      { status: 401 }
    );
  }
  if (!upstream.ok) {
    return NextResponse.json(
      { error: { code: "upstream_error", message: "Search is unavailable right now." } },
      { status: upstream.status === 400 ? 400 : 502 }
    );
  }

  try {
    return NextResponse.json(await upstream.json());
  } catch {
    return NextResponse.json(
      { error: { code: "upstream_error", message: "Search returned an empty response." } },
      { status: 502 }
    );
  }
}