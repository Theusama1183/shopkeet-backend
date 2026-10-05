"use server";

import { redirect } from "next/navigation";
import { headers, cookies } from "next/headers";

import { SESSION_COOKIE, sessionCookieOptions, authOriginFromHost } from "@/lib/session";

/**
 * Admin mutations go through server actions, never the browser's fetch: the
 * merchant JWT lives in an httpOnly cookie, so only server code can attach it
 * as the Authorization Bearer the Go API requires. Reads happen in server
 * components the same way (lib/admin.ts).
 */
export async function clearSessionAction() {
  const store = await cookies();
  store.set(SESSION_COOKIE, "", { ...sessionCookieOptions(), maxAge: 0 });

  const host = (await headers()).get("host") ?? "";
  redirect(`${authOriginFromHost(host)}/login`);
}