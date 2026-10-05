import type { Metadata } from "next";

import { AuthShell } from "@/components/auth/auth-shell";
import { ForgotPasswordFormClient } from "@/components/auth/forgot-password-form-client";
import { NewPasswordFormClient } from "@/components/auth/new-password-form-client";

export const metadata: Metadata = {
  title: "Reset your password · Shopkeet",
};

interface ResetPasswordPageProps {
  searchParams: Promise<{ token?: string; sent?: string }>;
}

export default async function ResetPasswordPage({ searchParams }: ResetPasswordPageProps) {
  const { token, sent } = await searchParams;

  if (token) {
    return (
      <AuthShell
        title="Set new password"
        description="Your reset link is valid. Choose a new password below."
      >
        <NewPasswordFormClient token={token} />
      </AuthShell>
    );
  }

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
      <ForgotPasswordFormClient sent={sent === "true"} />
    </AuthShell>
  );
}