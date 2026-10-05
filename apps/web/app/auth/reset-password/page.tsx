import type { Metadata } from "next";

import { AuthShell } from "@/components/auth/auth-shell";
import { PendingAuthFlow } from "@/components/auth/pending-auth-flow";

export const metadata: Metadata = {
  title: "Reset your password · Shopkeet",
};

export default function ResetPasswordPage() {
  return (
    <AuthShell
      title="Reset your password"
      description="We'll email you a link to get back into your store."
      footer={
        <span>
          Remembered it?{" "}
          <a className="font-medium text-foreground underline underline-offset-3" href="/login">
            Log in
          </a>
        </span>
      }
    >
      <PendingAuthFlow
        title="Password reset isn't wired up yet"
        description="Signup accepts email and password today, but there's no reset endpoint on the API yet. We'll flip this on once the email-versus-SMS decision for verification codes lands."
        href="/login"
        hrefLabel="Back to log in"
      />
    </AuthShell>
  );
}