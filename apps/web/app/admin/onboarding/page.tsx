import type { Metadata } from "next";
import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { StoreIcon } from "lucide-react";

import { authOriginFromHost, getSession } from "@/lib/session";
import { getTenantSettings } from "@/lib/store-actions";
import { PageHeader } from "@/components/admin/page-header";
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
    <div className="mx-auto w-full max-w-3xl space-y-5">
      <PageHeader
        title="Let's set up your store"
        icon={StoreIcon}
        description="You're signed in. Tell us how your shop should look and you're straight into the dashboard."
      />

      <OnboardingWizard settings={settings} />
    </div>
  );
}