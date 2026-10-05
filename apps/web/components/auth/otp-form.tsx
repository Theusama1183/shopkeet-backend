"use client";

import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon } from "lucide-react";

import { cn } from "@/lib/utils";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";

const otpSchema = z.object({
  code: z.string().trim().min(1, "Enter the code.").length(6, "Code must be 6 digits."),
});

type OtpValues = z.infer<typeof otpSchema>;

/**
 * What a successful verify answered — everything the caller needs to choose a
 * destination. The proxy fills mode in ("session" | "choose_store"); a single
 * store also carries onboarding_completed, which decides wizard vs dashboard.
 */
export interface OtpSuccess {
  mode?: "session" | "choose_store";
  onboarding_completed?: boolean;
}

interface OtpProps {
  email: string;
  onSuccess: (result: OtpSuccess) => void;
}

/**
 * The second factor. Email only: SMS is deliberately out of scope for now
 * (docs/13), so there is no channel switcher — just where the code went and
 * how to get another one.
 */
export function OtpForm({ email, onSuccess }: OtpProps) {
  const [pending, setPending] = React.useState(false);
  const [resendCooldown, setResendCooldown] = React.useState(0);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
    setValue,
  } = useForm<OtpValues>({
    resolver: zodResolver(otpSchema),
    defaultValues: { code: "" },
  });

  React.useEffect(() => {
    if (resendCooldown > 0) {
      const timer = setInterval(() => {
        setResendCooldown((c) => (c <= 1 ? 0 : c - 1));
      }, 1000);
      return () => clearInterval(timer);
    }
  }, [resendCooldown]);

  async function handleResend() {
    setPending(true);
    try {
      const res = await fetch("/api/auth/otp/send", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email, contactMethod: "email" }),
      });
      if (!res.ok) {
        const payload = (await res.json().catch(() => null)) as
          | { error?: { message?: string } }
          | null;
        apiErrorToast(new Error(payload?.error?.message ?? "Failed to resend code."));
        return;
      }
      setResendCooldown(60);
      setValue("code", "");
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
    } finally {
      setPending(false);
    }
  }

  async function onSubmit(values: OtpValues) {
    setPending(true);
    try {
      const res = await fetch("/api/auth/otp/verify", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email, code: values.code, contactMethod: "email" }),
      });
      const payload = (await res.json().catch(() => null)) as
        | ({ error?: { message?: string } } & OtpSuccess)
        | null;
      if (!res.ok) {
        setError("code", {
          type: "server",
          message: payload?.error?.message ?? "Invalid or expired code.",
        });
        return;
      }
      onSuccess(payload ?? {});
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className={cn("grid gap-4")} noValidate>
      <p className="text-sm text-muted-foreground">
        We sent a 6-digit code to <strong className="text-foreground">{email}</strong>. It expires
        in 10 minutes.
      </p>

      {errors.root ? (
        <Alert variant="destructive">
          <AlertDescription>{errors.root.message}</AlertDescription>
        </Alert>
      ) : null}

      <div className="grid gap-2">
        <Label htmlFor="otp-code">6-digit code</Label>
        <Input
          id="otp-code"
          type="text"
          inputMode="numeric"
          autoComplete="one-time-code"
          placeholder="123456"
          maxLength={6}
          aria-invalid={!!errors.code}
          {...register("code")}
        />
        {errors.code ? (
          <p className="text-caption text-destructive">{errors.code.message}</p>
        ) : null}
      </div>

      <Button type="submit" disabled={pending} className="w-full">
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Verifying…" : "Verify code"}
      </Button>

      <Button
        type="button"
        variant="link"
        onClick={handleResend}
        disabled={pending || resendCooldown > 0}
        className="w-full text-sm"
      >
        {resendCooldown > 0
          ? `Resend code in ${resendCooldown}s`
          : pending
          ? "Sending…"
          : "Didn't receive a code? Resend"}
      </Button>
    </form>
  );
}
