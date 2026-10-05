"use client";

import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon } from "lucide-react";

import { cn } from "@/lib/utils";
import { adminOrigin } from "@/lib/domains";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";

// No store field: the account is the identity, and the store list comes back
// from the API after the password checks out.
const loginSchema = z.object({
  email: z.string().trim().min(1, "Enter your email.").email("Enter a valid email."),
  password: z.string().min(1, "Enter your password."),
});

type LoginValues = z.infer<typeof loginSchema>;

interface LoginResult {
  mode?: "session" | "choose_store" | "otp_required";
  email?: string;
  error?: { code?: string; message?: string };
}

export function LoginForm() {
  const [pending, setPending] = React.useState(false);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { email: "", password: "" },
  });

  async function onSubmit(values: LoginValues) {
    setPending(true);
    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(values),
      });
      const payload = (await res.json().catch(() => null)) as LoginResult | null;
      if (!res.ok) {
        setError("root", {
          type: "server",
          message: payload?.error?.message ?? "Log in failed. Please try again.",
        });
        return;
      }
      // Password passed but verification is owed: park on the OTP page with
      // the address to send the code to (same host — /otp rewrites to /auth/otp).
      if (payload?.mode === "otp_required") {
        const params = new URLSearchParams({ email: payload.email ?? values.email, method: "email" });
        window.location.assign(`/otp?${params.toString()}`);
        return;
      }
      // Several stores: pick one on the store list. One store: straight through.
      // Either way the session cookie is already set — only the destination moves.
      const target =
        payload?.mode === "choose_store" ? `${adminOrigin()}/stores` : `${adminOrigin()}/`;
      window.location.assign(target);
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
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
        <Label htmlFor="email">Email</Label>
        <Input
          id="email"
          type="email"
          autoComplete="username"
          placeholder="you@company.com"
          aria-invalid={!!errors.email}
          {...register("email")}
        />
        {errors.email ? <p className="text-caption text-destructive">{errors.email.message}</p> : null}
      </div>

      <div className="grid gap-2">
        <Label htmlFor="password">Password</Label>
        <Input
          id="password"
          type="password"
          autoComplete="current-password"
          aria-invalid={!!errors.password}
          {...register("password")}
        />
        {errors.password ? (
          <p className="text-caption text-destructive">{errors.password.message}</p>
        ) : null}
      </div>

      <Button type="submit" disabled={pending} className="w-full">
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Logging in…" : "Log in"}
      </Button>
    </form>
  );
}