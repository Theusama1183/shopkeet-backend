"use client";

import { useRouter } from "next/navigation";

import { NewPasswordForm } from "@/components/auth/new-password-form";

interface NewPasswordFormClientProps {
  token: string;
}

export function NewPasswordFormClient({ token }: NewPasswordFormClientProps) {
  const router = useRouter();

  return (
    <NewPasswordForm
      token={token}
      onSuccess={() => {
        router.push("/login");
      }}
    />
  );
}