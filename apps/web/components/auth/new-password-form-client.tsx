"use client";

import { NewPasswordForm, type ResetSuccess } from "@/components/auth/new-password-form";
import { adminOrigin } from "@/lib/domains";

interface NewPasswordFormClientProps {
  token: string;
}

/**
 * After a reset the merchant is already signed in — the destination just has
 * to cross from the auth host to admin.<root>, which only a full navigation
 * can do (same reasoning as OtpFormClient): several stores → the picker,
 * incomplete onboarding → the wizard, otherwise the dashboard.
 */
export function NewPasswordFormClient({ token }: NewPasswordFormClientProps) {
  function done(result: ResetSuccess) {
    if (result.mode === "choose_store") {
      window.location.assign(`${adminOrigin()}/stores`);
      return;
    }
    if (result.onboarding_completed === false) {
      window.location.assign(`${adminOrigin()}/onboarding`);
      return;
    }
    window.location.assign(`${adminOrigin()}/`);
  }

  return <NewPasswordForm token={token} onSuccess={done} />;
}
