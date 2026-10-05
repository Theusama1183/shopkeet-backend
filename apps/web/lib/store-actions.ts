"use server";

import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";

import { adminMutation, adminRequest } from "@/lib/admin-api";
import { ApiError, apiFieldErrors, apiRequest } from "@/lib/api";
import type { MerchantStore, TenantSettings, TenantSettingsResponse } from "@/lib/admin-types";
import {
  SESSION_COOKIE,
  STORE_PICK_COOKIE,
  authOriginFromHost,
  getSession,
  sessionCookieOptions,
  SESSION_MAX_AGE_SECONDS,
} from "@/lib/session";

/**
 * Store-profile writes for the one-time onboarding wizard.
 *
 * Both actions run server-side because the merchant JWT is httpOnly — only server
 * code can attach it as the Bearer the Go API requires. The wizard submits once
 * and gets one answer back: either per-field messages the form renders inline, or
 * a redirect into the dashboard.
 */

export interface StoreProfileInput {
  name: string;
  subdomain: string;
  default_currency: string;
  timezone: string;
  support_email: string;
  tax_rate_percent: string;
}

export type WizardResult =
  | { ok: true }
  | { ok: false; message: string; fields: Record<string, string> };

/** Reads the store's current settings for the wizard's initial values. */
export async function getTenantSettings(): Promise<TenantSettings> {
  const data = await adminRequest<TenantSettingsResponse>("/tenant/settings");
  return data.settings;
}

/**
 * Lists the stores this account can open.
 *
 * This cannot go through adminRequest: that helper demands a merchant session, but
 * a merchant who owns several stores arrives here straight after login with only
 * the store_pick ticket — there is no tenant-scoped session until a store is
 * chosen. An existing session also works, which is how the sidebar's switcher
 * reuses this page.
 */
export async function getMerchantStores(): Promise<MerchantStore[]> {
  const jar = await cookies();
  const session = await getSession();
  const credential = jar.get(STORE_PICK_COOKIE)?.value ?? session?.token;

  if (!credential) {
    const host = (await headers()).get("host") ?? "";
    redirect(`${authOriginFromHost(host)}/login`);
  }

  const data = await apiRequest<{ stores: MerchantStore[] }>("/auth/stores", {
    token: credential,
  });
  return data.stores;
}

/**
 * Saves the wizard's profile fields. Unlike a settings-page save this can fail on
 * a taken store link, so the 409 comes back as a field message on `subdomain`
 * rather than a toast — the merchant is mid-form and must not lose their input.
 */
export async function saveStoreProfileAction(input: StoreProfileInput): Promise<WizardResult> {
  const taxRate = Number.parseInt(input.tax_rate_percent, 10);

  try {
    await adminMutation<TenantSettingsResponse>("/tenant/settings", {
      method: "PATCH",
      body: {
        name: input.name.trim(),
        subdomain: input.subdomain.trim().toLowerCase(),
        default_currency: input.default_currency,
        timezone: input.timezone,
        support_email: input.support_email.trim(),
        // An empty tax field means "no tax", which is a real 0 — not a null the
        // API would read as "leave it alone".
        tax_rate_percent: Number.isNaN(taxRate) ? 0 : taxRate,
      },
    });
  } catch (error) {
    if (error instanceof ApiError) {
      if (error.status === 409) {
        return {
          ok: false,
          message: error.message,
          fields: { subdomain: error.message },
        };
      }
      const fields = apiFieldErrors(error);
      if (fields) return { ok: false, message: error.message, fields };
      return { ok: false, message: error.message, fields: {} };
    }
    return {
      ok: false,
      message: "Could not save your store. Check your connection and try again.",
      fields: {},
    };
  }

  return { ok: true };
}

/**
 * Closes the wizard for good and swaps in the refreshed session the API returns.
 *
 * That last part is the point: the old cookie carries onboarding_completed=false,
 * so the admin layout would bounce the merchant straight back here. Re-setting the
 * cookie is what lets them through.
 */
export async function completeOnboardingAction(): Promise<void> {
  const data = await adminMutation<{
    token: string;
    onboarding_completed: boolean;
  }>("/tenant/onboarding/complete", { method: "POST" });

  const jar = await cookies();
  jar.set(SESSION_COOKIE, data.token, {
    ...sessionCookieOptions(),
    maxAge: SESSION_MAX_AGE_SECONDS,
  });

  redirect("/admin");
}