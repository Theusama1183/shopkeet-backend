import type { Metadata } from "next";

import { AuthShell } from "@/components/auth/auth-shell";
import { LoginForm } from "@/components/auth/login-form";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Log in · Shopkeet",
};

export default function LoginPage() {
  return (
    <AuthShell
      title="Welcome back"
      description="Manage your catalog, orders, and customers from one dashboard."
      footer={
        <span>
          New to Shopkeet?{" "}
          <Link className="font-medium text-foreground underline underline-offset-3" href="/signup">
            Create an account
          </Link>
        </span>
      }
    >
      <LoginForm />
    </AuthShell>
  );
}