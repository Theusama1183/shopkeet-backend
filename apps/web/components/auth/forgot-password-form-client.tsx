"use client";

import { useRouter } from "next/navigation";

import { ForgotPasswordForm } from "@/components/auth/forgot-password-form";

interface ForgotPasswordFormClientProps {
  sent?: boolean;
}

/**
 * sent=true (the same page after a submit) renders a confirmation instead of
 * the form. It must NOT navigate from the render body — pushing a route while
 * rendering re-enters the component with the same flag and loops. Navigation
 * here only happens on user actions: the submit callback, or a link.
 */
export function ForgotPasswordFormClient({ sent }: ForgotPasswordFormClientProps) {
  const router = useRouter();

  if (sent) {
    return (
      <div className="grid gap-4 text-center">
        <p className="text-sm text-muted-foreground">
          Check your inbox — if an account exists for that address, a reset link is on its way.
          The link works once and expires in an hour.
        </p>
        <div className="flex items-center justify-center gap-4 text-sm">
          <a className="font-medium text-foreground underline underline-offset-3" href="/login">
            Back to log in
          </a>
          <button
            type="button"
            className="font-medium text-foreground underline underline-offset-3"
            onClick={() => router.replace("/reset-password")}
          >
            Use a different email
          </button>
        </div>
      </div>
    );
  }

  return (
    <ForgotPasswordForm
      onSuccess={() => {
        router.push("/reset-password?sent=true");
      }}
    />
  );
}
