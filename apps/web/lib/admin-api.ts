import { cache } from "react";
import { headers } from "next/headers";
import { redirect } from "next/navigation";

import { getSession, authOriginFromHost } from "@/lib/session";
import { ApiError } from "@/lib/api";

/**
 * Server-only data layer for the admin surface. Every read here pulls the
 * merchant JWT out of the httpOnly session cookie (getSession) and forwards it
 * as the Authorization Bearer the Go API demands — the browser never holds the
 * token. Pages run these in server components; interactive writes go through
 * server actions (lib/admin-actions.ts, lib/order-actions.ts).
 */

export const ADMIN_API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080/api/v1";

function toAdminError(status: number, code: string, message: string): ApiError {
  return new ApiError({ status, code, message });
}

async function readToken(): Promise<{ token: string; tenantId: string }> {
  const session = await getSession();
  if (!session) {
    const host = (await headers()).get("host") ?? "";
    redirect(`${authOriginFromHost(host)}/login`);
  }
  return { token: session.token, tenantId: session.claims.tenant_id };
}

async function adminFetch(path: string, init?: RequestInit): Promise<Response> {
  const { token, tenantId } = await readToken();
  let response: Response;
  try {
    response = await fetch(`${ADMIN_API_BASE}${path}`, {
      ...init,
      headers: {
        authorization: `Bearer ${token}`,
        // Public storefront reads (products, shipping rates) resolve the tenant
        // from X-Tenant-ID; admin routes ignore it and use the JWT instead.
        "x-tenant-id": tenantId,
        ...(init?.headers as Record<string, string> | undefined),
      },
      cache: "no-store",
    });
  } catch {
    throw toAdminError(
      503,
      "network_error",
      "Could not reach the Shopkeet API. Check your connection."
    );
  }
  return response;
}

export interface AdminErrorResponse {
  error?: { code?: string; message?: string };
}

export const adminRequest = cache(
  async <T>(
    path: string,
    init?: RequestInit
  ): Promise<T> => {
    const response = await adminFetch(path, init);

    if (!response.ok) {
      let code = "request_failed";
      let message = `Request failed with status ${response.status}.`;
      try {
        const payload = (await response.json()) as AdminErrorResponse;
        if (payload?.error?.code) code = payload.error.code;
        if (payload?.error?.message) message = payload.error.message;
      } catch {
        // Non-JSON error body — keep the generic copy above.
      }
      throw toAdminError(response.status, code, message);
    }

    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }
);

export type { ApiError };

/**
 * Performs an admin write (server action path). Deliberately NOT wrapped in
 * React cache() — mutations must never hit the request-memoized helper, or a
 * read of the same URL could hand back last render's body.
 */
export async function adminMutation<T>(
  path: string,
  init: Omit<RequestInit, "body"> & { body?: unknown }
): Promise<T> {
  const response = await adminFetch(path, {
    ...init,
    method: init.method ?? "POST",
    headers: {
      "content-type": "application/json",
      ...(init?.headers as Record<string, string> | undefined),
    },
    body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
  });

  if (!response.ok) {
    let code = "request_failed";
    let message = `Request failed with status ${response.status}.`;
    try {
      const payload = (await response.json()) as AdminErrorResponse;
      if (payload?.error?.code) code = payload.error.code;
      if (payload?.error?.message) message = payload.error.message;
    } catch {
      // Non-JSON error body — keep the generic copy above.
    }
    throw toAdminError(response.status, code, message);
  }

  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}