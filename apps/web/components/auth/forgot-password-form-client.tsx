"use client";

import { useRouter } from "next/navigation";

import { ForgotPasswordForm } from "@/components/auth/forgot-password-form";

interface ForgotPasswordFormClientProps {
  sent?: boolean;
}

export function ForgotPasswordFormClient({ sent }: ForgotPasswordFormClientProps) {
  const router = useRouter();

  if (sent) {
    router.push("/auth/reset-password?sent=true");
    return null;
  }

  return (
    <ForgotPasswordForm
      onSuccess={() => {
        router.push("/auth/reset-password?sent=true");
      }}
    />
  );
}