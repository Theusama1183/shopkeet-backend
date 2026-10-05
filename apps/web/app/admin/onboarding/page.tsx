import type { Metadata } from "next";
import { headers } from "next/headers";
import { redirect } from "next/navigation";

import { authOriginFromHost, getSession } from "@/lib/session";
import { getTenantSettings } from "@/lib/store-actions";
import { OnboardingWizard } from "@/components/admin/onboarding-wizard";

export const metadata: Metadata = {
  title: "Set up your store · Shopkeet",
};

/**
 * The one-time store wizard. Signup mints a session flagged
 * onboarding_completed=false, and the (guarded) admin layout sends it here, so
 * this page is reachable exactly once per store.
 *
 * The already-finished check runs off the session's own claim, before any API
 * call: it is the common case, it keeps a completed merchant out of here even
 * when the API is unreachable, and it guarantees this page can never bounce back
 * to the layout that bounces to it. The API's copy of the flag is then read as
 * the source of truth for prefilling (and for a flag flipped in another tab).
 */
export default async function OnboardingPage() {
  const host = (await headers()).get("host") ?? "";
  const session = await getSession();
  if (!session) redirect(`${authOriginFromHost(host)}/login`);
  // Absent means an older token, which counts as already onboarded.
  if (session.claims.onboarding_completed !== false) redirect("/admin");

  const settings = await getTenantSettings();
  if (settings.onboarding_completed) redirect("/admin");

  return (
    <div className="mx-auto w-full max-w-3xl space-y-8">
      <header className="space-y-3 text-center sm:text-left">
        <p className="text-caption font-medium uppercase tracking-widest text-primary">
          One last step
        </p>
        <h1 className="text-3xl font-semibold tracking-tight">Let&apos;s set up your store</h1>
        <p className="text-lg text-muted-foreground">
          You&apos;re signed in. Tell us how your shop should look and you&apos;re straight
          into the dashboard.
        </p>
      </header>

      <OnboardingWizard settings={settings} />
    </div>
  );
}