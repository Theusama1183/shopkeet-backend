"use client";

import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon, MailIcon, PhoneIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

const otpSchema = z.object({
  code: z.string().trim().min(1, "Enter the code.").length(6, "Code must be 6 digits."),
});

type OtpValues = z.infer<typeof otpSchema>;

interface OtpProps {
  email: string;
  contactMethod: "email" | "sms";
  onSuccess: () => void;
}

export function OtpForm({ email, contactMethod, onSuccess }: OtpProps) {
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
        body: JSON.stringify({ email, contactMethod }),
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
        body: JSON.stringify({ email, code: values.code, contactMethod }),
      });
      const payload = (await res.json().catch(() => null)) as
        | { error?: { message?: string }; mode?: string }
        | null;
      if (!res.ok) {
        setError("code", {
          type: "server",
          message: payload?.error?.message ?? "Invalid or expired code.",
        });
        return;
      }
      onSuccess();
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className={cn("grid gap-4")} noValidate>
      <div className="grid gap-2">
        <Label>Verification code sent via</Label>
        <Tabs defaultValue={contactMethod} className="w-full">
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="email">
              <MailIcon className="mr-2 h-4 w-4" aria-hidden="true" />
              Email
            </TabsTrigger>
            <TabsTrigger value="sms">
              <PhoneIcon className="mr-2 h-4 w-4" aria-hidden="true" />
              SMS
            </TabsTrigger>
          </TabsList>
          <TabsContent value="email" className="mt-2 p-0">
            <p className="text-sm text-muted-foreground">
              Code sent to <strong>{email}</strong>
            </p>
          </TabsContent>
          <TabsContent value="sms" className="mt-2 p-0">
            <p className="text-sm text-muted-foreground">
              Code sent via SMS (number on file)
            </p>
          </TabsContent>
        </Tabs>
      </div>

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