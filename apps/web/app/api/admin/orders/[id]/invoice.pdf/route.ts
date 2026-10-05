import { NextResponse } from "next/server";

import { ADMIN_API_BASE } from "@/lib/admin-api";
import { getSession, authOriginFromHost } from "@/lib/session";
import { headers } from "next/headers";

/**
 * Proxy for invoice.pdf. The storefront can't attach the Bearer token — the JWT
 * lives in an httpOnly cookie — so the browser hits this route and this handler
 * forwards the cookie's token to the Go API.
 */
export async function GET(
  _req: Request,
  ctx: { params: Promise<{ id: string }> }
) {
  const { id } = await ctx.params;
  const session = await getSession();

  if (!session) {
    const host = (await headers()).get("host") ?? "";
    return NextResponse.redirect(`${authOriginFromHost(host)}/login`);
  }

  let upstream: Response;
  try {
    upstream = await fetch(`${ADMIN_API_BASE}/orders/${id}/invoice.pdf`, {
      headers: { authorization: `Bearer ${session.token}` },
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: { code: "network_error", message: "Could not reach the Shopkeet API." } },
      { status: 502 }
    );
  }

  if (!upstream.ok) {
    if (upstream.status === 404) {
      return NextResponse.json({ error: { code: "not_found", message: "Invoice not found." } }, { status: 404 });
    }
    return NextResponse.json(
      { error: { code: "upstream_error", message: "The invoice could not be generated." } },
      { status: upstream.status === 401 || upstream.status === 403 ? 401 : 502 }
    );
  }

  let body: ArrayBuffer;
  try {
    body = await upstream.arrayBuffer();
  } catch {
    return NextResponse.json({ error: { code: "upstream_error", message: "Empty invoice response." } }, { status: 502 });
  }

  return new NextResponse(body, {
    status: 200,
    headers: {
      "content-type": upstream.headers.get("content-type") ?? "application/pdf",
      "content-length": String(body.byteLength),
      "content-disposition": `inline; filename="invoice-${id.slice(0, 8)}.pdf"`,
    },
  });
}