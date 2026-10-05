import type { Metadata } from "next";

import { AuthShell } from "@/components/auth/auth-shell";
import { SignupForm } from "@/components/auth/signup-form";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Create your store · Shopkeet",
};

export default function SignupPage() {
  return (
    <AuthShell
      title="Create your account"
      description="Free to start. Name your store and publish in the next step."
      footer={
        <span>
          Already have an account?{" "}
          <Link className="font-medium text-foreground underline underline-offset-3" href="/login">
            Log in
          </Link>
        </span>
      }
    >
      <SignupForm />
    </AuthShell>
  );
}