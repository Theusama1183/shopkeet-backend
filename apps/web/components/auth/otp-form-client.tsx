"use client";

import { useRouter } from "next/navigation";

import { OtpForm } from "@/components/auth/otp-form";

interface OtpFormClientProps {
  email: string;
  contactMethod: "email" | "sms";
}

export function OtpFormClient({ email, contactMethod }: OtpFormClientProps) {
  const router = useRouter();

  return (
    <OtpForm
      email={email}
      contactMethod={contactMethod}
      onSuccess={() => {
        router.push("/admin/");
      }}
    />
  );
}