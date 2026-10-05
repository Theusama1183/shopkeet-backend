"use client";

import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon, EyeIcon, EyeOffIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";

const PASSWORD_HINTS = ["At least 8 characters", "Letters, numbers, and symbols"];

const newPasswordSchema = z
  .object({
    password: z.string().min(8, "Use at least 8 characters."),
    confirm: z.string().min(1, "Confirm your password."),
  })
  .refine((data) => data.password === data.confirm, {
    message: "Passwords do not match.",
    path: ["confirm"],
  });

type NewPasswordValues = z.infer<typeof newPasswordSchema>;

interface NewPasswordFormProps {
  token: string;
  onSuccess: () => void;
}

export function NewPasswordForm({ token, onSuccess }: NewPasswordFormProps) {
  const [pending, setPending] = React.useState(false);
  const [showPassword, setShowPassword] = React.useState(false);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<NewPasswordValues>({
    resolver: zodResolver(newPasswordSchema),
    defaultValues: { password: "", confirm: "" },
  });

  async function onSubmit(values: NewPasswordValues) {
    setPending(true);
    try {
      const res = await fetch("/api/auth/reset-password", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ token, password: values.password }),
      });
      const payload = (await res.json().catch(() => null)) as
        | { error?: { message?: string } }
        | null;
      if (!res.ok) {
        setError("root", {
          type: "server",
          message: payload?.error?.message ?? "Failed to reset password. Link may have expired.",
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
        <Label htmlFor="new-password">New password</Label>
        <div className="relative">
          <Input
            id="new-password"
            type={showPassword ? "text" : "password"}
            autoComplete="new-password"
            placeholder="Create a new password"
            aria-invalid={!!errors.password}
            {...register("password")}
          />
          <button
            type="button"
            onClick={() => setShowPassword(!showPassword)}
            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            aria-label={showPassword ? "Hide password" : "Show password"}
          >
            {showPassword ? <EyeOffIcon className="h-4 w-4" /> : <EyeIcon className="h-4 w-4" />}
          </button>
        </div>
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

      <div className="grid gap-2">
        <Label htmlFor="confirm-password">Confirm password</Label>
        <Input
          id="confirm-password"
          type={showPassword ? "text" : "password"}
          autoComplete="new-password"
          placeholder="Confirm your new password"
          aria-invalid={!!errors.confirm}
          {...register("confirm")}
        />
        {errors.confirm ? (
          <p className="text-caption text-destructive">{errors.confirm.message}</p>
        ) : null}
      </div>

      <Button type="submit" disabled={pending} className="w-full">
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Resetting password…" : "Reset password"}
      </Button>

      <p className="text-center text-caption text-muted-foreground">
        <a className="font-medium text-foreground underline underline-offset-3" href="/login">
          Back to log in
        </a>
      </p>
    </form>
  );
}