"use client";

import { OtpForm, type OtpSuccess } from "@/components/auth/otp-form";
import { adminOrigin } from "@/lib/domains";

interface OtpFormClientProps {
  email: string;
}

/**
 * Landing spot after a verified code. The verify response lives on the auth
 * host, but the destination is on admin.<root> — only a full cross-origin
 * navigation can carry it, so every branch builds an absolute admin URL from
 * adminOrigin() rather than pushing through the router (which cannot cross
 * subdomains, and whose paths would be admin-host-relative at best).
 *
 * - several stores → the picker (the store_pick ticket is already in its cookie);
 * - onboarding incomplete → the wizard;
 * - otherwise → the dashboard.
 */
export function OtpFormClient({ email }: OtpFormClientProps) {
  function done(result: OtpSuccess) {
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

  return <OtpForm email={email} onSuccess={done} />;
}
