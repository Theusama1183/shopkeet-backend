"use client";

import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon, MailIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";

const forgotSchema = z.object({
  email: z.string().trim().min(1, "Enter your email.").email("Enter a valid email."),
});

type ForgotValues = z.infer<typeof forgotSchema>;

interface ForgotPasswordFormProps {
  onSuccess: () => void;
}

export function ForgotPasswordForm({ onSuccess }: ForgotPasswordFormProps) {
  const [pending, setPending] = React.useState(false);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<ForgotValues>({
    resolver: zodResolver(forgotSchema),
    defaultValues: { email: "" },
  });

  async function onSubmit(values: ForgotValues) {
    setPending(true);
    try {
      const res = await fetch("/api/auth/forgot-password", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(values),
      });
      const payload = (await res.json().catch(() => null)) as
        | { error?: { message?: string } }
        | null;
      if (!res.ok) {
        setError("root", {
          type: "server",
          message: payload?.error?.message ?? "Failed to send reset link. Please try again.",
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
      {errors.root ? (
        <Alert variant="destructive">
          <AlertDescription>{errors.root.message}</AlertDescription>
        </Alert>
      ) : null}

      <div className="grid gap-2">
        <Label htmlFor="reset-email">Email</Label>
        <Input
          id="reset-email"
          type="email"
          autoComplete="email"
          placeholder="you@company.com"
          aria-invalid={!!errors.email}
          {...register("email")}
        />
        {errors.email ? (
          <p className="text-caption text-destructive">{errors.email.message}</p>
        ) : (
          <p className="text-caption text-muted-foreground">
            We&apos;ll send a password reset link to this address.
          </p>
        )}
      </div>

      <Button type="submit" disabled={pending} className="w-full">
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Sending reset link…" : "Send reset link"}
      </Button>

      <p className="text-center text-caption text-muted-foreground">
        <MailIcon className="inline h-3 w-3 mr-1" aria-hidden="true" />
        Didn&apos;t receive the email?{" "}
        <a className="font-medium text-foreground underline underline-offset-3" href="/login">
          Log in
        </a>
      </p>
    </form>
  );
}