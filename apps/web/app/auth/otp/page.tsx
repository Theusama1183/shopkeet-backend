import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { AuthShell } from "@/components/auth/auth-shell";
import { OtpFormClient } from "@/components/auth/otp-form-client";

export const metadata: Metadata = {
  title: "Verify your account · Shopkeet",
};

interface OtpPageProps {
  searchParams: Promise<{ email?: string; method?: "email" | "sms" }>;
}

export default async function OtpPage({ searchParams }: OtpPageProps) {
  const { email, method } = await searchParams;

  if (!email) {
    redirect("/login");
  }

  const contactMethod = method === "sms" ? "sms" : "email";

  return (
    <AuthShell
      title="Verify your account"
      description="Enter the 6-digit code we sent to complete your login."
      footer={
        <span>
          No code yet?{" "}
          <a className="font-medium text-foreground underline underline-offset-3" href="/login">
            Go back to log in
          </a>
          .
        </span>
      }
    >
      <OtpFormClient
        email={email}
        contactMethod={contactMethod}
      />
    </AuthShell>
  );
}