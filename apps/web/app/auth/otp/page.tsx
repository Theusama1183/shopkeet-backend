import type { Metadata } from "next";

import { AuthShell } from "@/components/auth/auth-shell";
import { PendingAuthFlow } from "@/components/auth/pending-auth-flow";

export const metadata: Metadata = {
  title: "Verify your account · Shopkeet",
};

export default function OtpPage() {
  return (
    <AuthShell
      title="Verify your account"
      description="Enter the code we sent you to finish logging in."
      footer={
        <span>
          No code yet? Go back to{" "}
          <a className="font-medium text-foreground underline underline-offset-3" href="/login">
            log in
          </a>
          .
        </span>
      }
    >
      <PendingAuthFlow
        title="Two-factor codes aren't live yet"
        description="The API doesn't deliver OTP codes yet — the Email-versus-SMS decision is still open (see the 13-spec). Until then, email and password keep working as usual."
        href="/login"
      />
    </AuthShell>
  );
}