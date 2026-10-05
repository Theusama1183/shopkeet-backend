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

// The account only. Store name, store link, currency and support details are
// collected in the one-time wizard right after this, which is a far better place
// to ask for them than a sign-up form.
const signupSchema = z.object({
  email: z.string().trim().min(1, "Enter your email.").email("Enter a valid email."),
  password: z.string().min(8, "Use at least 8 characters."),
});

type SignupValues = z.infer<typeof signupSchema>;

const PASSWORD_HINTS = ["At least 8 characters", "Letters, numbers, and symbols"];

export function SignupForm() {
  const [pending, setPending] = React.useState(false);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<SignupValues>({
    resolver: zodResolver(signupSchema),
    defaultValues: { email: "", password: "" },
  });

  async function onSubmit(values: SignupValues) {
    setPending(true);
    try {
      const res = await fetch("/api/auth/signup", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(values),
      });
      if (!res.ok) {
        const payload = (await res.json().catch(() => null)) as
          | { error?: { code?: string; message?: string } }
          | null;
        setError("root", {
          type: "server",
          message: payload?.error?.message ?? "Signup failed. Please try again.",
        });
        return;
      }
      // The account exists and is signed in, but its store is still unnamed, so the
      // wizard it is. Built by concatenation because this is a cross-origin jump
      // (auth host -> admin host) — a client router push cannot do it, and the URL
      // only exists at runtime.
      const destination = adminOrigin() + "/onboarding";
      window.location.assign(destination);
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
        <Label htmlFor="signup-email">Email</Label>
        <Input
          id="signup-email"
          type="email"
          autoComplete="email"
          placeholder="you@company.com"
          aria-invalid={!!errors.email}
          {...register("email")}
        />
        {errors.email ? <p className="text-caption text-destructive">{errors.email.message}</p> : null}
      </div>

      <div className="grid gap-2">
        <Label htmlFor="signup-password">Password</Label>
        <Input
          id="signup-password"
          type="password"
          autoComplete="new-password"
          aria-invalid={!!errors.password}
          {...register("password")}
        />
        {errors.password ? (
          <p className="text-caption text-destructive">{errors.password.message}</p>
        ) : (
          <ul className="list-inside list-disc text-caption text-muted-foreground">
            {PASSWORD_HINTS.map((hint) => (
              <li key={hint}>{hint}</li>
            ))}
          </ul>
        )}
      </div>

      <Button type="submit" disabled={pending} className="w-full">
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Creating your account…" : "Create account"}
      </Button>
    </form>
  );
}