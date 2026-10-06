"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { ArrowRightIcon, Loader2Icon } from "lucide-react";

import { ROOT_DOMAIN } from "@/lib/domains";
import type { TenantSettings } from "@/lib/admin-types";
import { completeOnboardingAction, saveStoreProfileAction } from "@/lib/store-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Alert, AlertDescription } from "@/components/ui/alert";

// Currencies the API's default_currency column can hold, kept in step with the
// storefront's formatting helpers.
const CURRENCIES = [
  { value: "USD", label: "US Dollar (USD)" },
  { value: "EUR", label: "Euro (EUR)" },
  { value: "GBP", label: "British Pound (GBP)" },
  { value: "PKR", label: "Pakistani Rupee (PKR)" },
  { value: "INR", label: "Indian Rupee (INR)" },
  { value: "AED", label: "UAE Dirham (AED)" },
  { value: "SAR", label: "Saudi Riyal (SAR)" },
  { value: "CAD", label: "Canadian Dollar (CAD)" },
  { value: "AUD", label: "Australian Dollar (AUD)" },
];

const TIMEZONES = [
  { value: "UTC", label: "UTC" },
  { value: "Asia/Karachi", label: "Karachi · Islamabad · Lahore" },
  { value: "Asia/Kolkata", label: "India · Delhi · Mumbai" },
  { value: "Asia/Dubai", label: "Dubai · Gulf" },
  { value: "Europe/London", label: "London" },
  { value: "Europe/Berlin", label: "Berlin · Central Europe" },
  { value: "America/New_York", label: "New York · Eastern" },
  { value: "America/Chicago", label: "Chicago · Central" },
  { value: "America/Los_Angeles", label: "Los Angeles · Pacific" },
];

const profileSchema = z.object({
  name: z.string().trim().min(1, "Give your store a name.").max(80, "Keep the name under 80 characters."),
  subdomain: z
    .string()
    .trim()
    .min(3, "Use at least 3 characters.")
    .max(63, "Keep the store link under 63 characters.")
    .regex(/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/, "Lowercase letters, numbers, and hyphens only."),
  default_currency: z.string().min(1, "Pick a currency."),
  timezone: z.string().min(1, "Pick a timezone."),
  support_email: z.string().trim().email("Enter a valid email.").or(z.literal("")),
  tax_rate_percent: z
    .string()
    .regex(/^\d{1,2}(\.\d{1,2})?$/, "Use a number like 0 or 17.5.")
    .refine((v) => Number.parseFloat(v) <= 100, "Tax can't be over 100%."),
});

type ProfileValues = z.infer<typeof profileSchema>;

/** Turns the store name into a first suggestion for the store link. */
function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

export function OnboardingWizard({ settings }: { settings: TenantSettings }) {
  const router = useRouter();
  const [pending, setPending] = React.useState(false);
  const [serverError, setServerError] = React.useState<string | null>(null);
  const [slugTouched, setSlugTouched] = React.useState(false);

  const {
    register,
    handleSubmit,
    setError,
    control,
    setValue,
    formState: { errors },
  } = useForm<ProfileValues>({
    resolver: zodResolver(profileSchema),
    defaultValues: {
      name: settings.name === "My store" ? "" : settings.name,
      subdomain: settings.subdomain.startsWith("store-") ? "" : settings.subdomain,
      default_currency: settings.default_currency || "USD",
      timezone: settings.timezone || "UTC",
      support_email: settings.support_email || "",
      tax_rate_percent: settings.tax_rate_percent ? String(settings.tax_rate_percent) : "0",
    },
  });

  // Registered once at the top level so the store-link input can wrap RHF's
  // onChange to note that the merchant has taken over the suggestion.
  const subdomainField = register("subdomain");
  const nameField = register("name");
  const supportEmailField = register("support_email");
  const taxField = register("tax_rate_percent");

  const name = useWatch({ control, name: "name" });
  const subdomain = useWatch({ control, name: "subdomain" });
  const selectedCurrency = useWatch({ control, name: "default_currency" });
  const selectedTimezone = useWatch({ control, name: "timezone" });

  // Suggest the store link from the store name until the merchant edits it
  // themselves — after that their choice wins, even if they rename the store.
  React.useEffect(() => {
    if (slugTouched) return;
    const suggestion = slugify(name);
    if (suggestion.length >= 3) setValue("subdomain", suggestion);
  }, [name, slugTouched, setValue]);

  async function onSubmit(values: ProfileValues) {
    setPending(true);
    setServerError(null);

    const saved = await saveStoreProfileAction({
      name: values.name,
      subdomain: values.subdomain,
      default_currency: values.default_currency,
      timezone: values.timezone,
      support_email: values.support_email,
      tax_rate_percent: values.tax_rate_percent,
    });

    if (!saved.ok) {
      // A taken store link is the one failure a merchant can fix themselves, so
      // it lands on the field. Everything else surfaces once, above the form.
      for (const [field, message] of Object.entries(saved.fields)) {
        setError(field as keyof ProfileValues, { type: "server", message });
      }
      if (Object.keys(saved.fields).length === 0) setServerError(saved.message);
      setPending(false);
      return;
    }

    // Completing flips onboarding server-side and re-mints the session, which
    // redirect() uses to drop the new cookie into place before the dashboard
    // renders. refresh() first in case the action's redirect is treated as a
    // no-op for a client component.
    await completeOnboardingAction();
    router.refresh();
  }

  const linkPreview = subdomain ? `${subdomain}.${ROOT_DOMAIN}` : `yourstore.${ROOT_DOMAIN}`;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="grid gap-8" noValidate>
      {serverError ? (
        <Alert variant="destructive">
          <AlertDescription>{serverError}</AlertDescription>
        </Alert>
      ) : null}

      <Card className="overflow-hidden">
        <CardHeader className="border-b bg-muted/40 pb-6">
          <CardTitle className="text-h2">Name your store</CardTitle>
          <CardDescription className="mt-1">
            This is how customers will find you. You can change any of it later in
            Settings.
          </CardDescription>
        </CardHeader>

        <CardContent className="grid gap-6 pt-6">
          <div className="grid gap-2">
            <Label htmlFor="wizard-name" className="text-base">
              Store name
            </Label>
            <Input
              id="wizard-name"
              className="h-11 text-base"
              autoComplete="organization"
              placeholder="Aurora Coffee Roasters"
              aria-invalid={!!errors.name}
              {...nameField}
            />
            {errors.name ? (
              <p className="text-caption text-destructive">{errors.name.message}</p>
            ) : null}
          </div>

          <div className="grid gap-2">
            <Label htmlFor="wizard-subdomain" className="text-base">
              Store link
            </Label>
            <div className="flex items-center gap-0">
              <Input
                id="wizard-subdomain"
                className="h-11 rounded-r-none border-r-0 text-base"
                placeholder="aurora-coffee"
                autoCapitalize="none"
                spellCheck={false}
                aria-invalid={!!errors.subdomain}
                {...subdomainField}
                onChange={(event) => {
                  setSlugTouched(true);
                  subdomainField.onChange(event);
                }}
              />
              <span className="inline-flex h-11 items-center rounded-r-lg border border-input bg-muted/50 px-3 text-sm text-muted-foreground">
                .{ROOT_DOMAIN}
              </span>
            </div>
            {errors.subdomain ? (
              <p className="text-caption text-destructive">{errors.subdomain.message}</p>
            ) : (
              <p className="text-caption text-muted-foreground">
                Your store will live at{" "}
                <span className="font-medium text-foreground">{linkPreview}</span>
              </p>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="border-b bg-muted/40 pb-6">
          <CardTitle className="text-h2">How you sell</CardTitle>
          <CardDescription className="mt-1">
            Prices, tax and contact details for customer receipts and notifications.
          </CardDescription>
        </CardHeader>

        <CardContent className="grid gap-6 pt-6 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="wizard-currency" className="text-base">
              Currency
            </Label>
            <Select
              value={selectedCurrency}
              onValueChange={(value) => setValue("default_currency", value, { shouldDirty: true })}
            >
              <SelectTrigger id="wizard-currency" className="h-11 text-base">
                <SelectValue placeholder="Pick a currency" />
              </SelectTrigger>
              <SelectContent>
                {CURRENCIES.map((currency) => (
                  <SelectItem key={currency.value} value={currency.value} className="text-base">
                    {currency.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {errors.default_currency ? (
              <p className="text-caption text-destructive">{errors.default_currency.message}</p>
            ) : null}
          </div>

          <div className="grid gap-2">
            <Label htmlFor="wizard-timezone" className="text-base">
              Timezone
            </Label>
            <Select
              value={selectedTimezone}
              onValueChange={(value) => setValue("timezone", value, { shouldDirty: true })}
            >
              <SelectTrigger id="wizard-timezone" className="h-11 text-base">
                <SelectValue placeholder="Pick a timezone" />
              </SelectTrigger>
              <SelectContent>
                {TIMEZONES.map((timezone) => (
                  <SelectItem key={timezone.value} value={timezone.value} className="text-base">
                    {timezone.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {errors.timezone ? (
              <p className="text-caption text-destructive">{errors.timezone.message}</p>
            ) : null}
          </div>

          <div className="grid gap-2">
            <Label htmlFor="wizard-support-email" className="text-base">
              Support email
              <span className="ml-2 text-sm font-normal text-muted-foreground">optional</span>
            </Label>
            <Input
              id="wizard-support-email"
              className="h-11 text-base"
              type="email"
              autoComplete="email"
              placeholder="help@auroracoffee.com"
              aria-invalid={!!errors.support_email}
              {...supportEmailField}
            />
            {errors.support_email ? (
              <p className="text-caption text-destructive">{errors.support_email.message}</p>
            ) : null}
          </div>

          <div className="grid gap-2">
            <Label htmlFor="wizard-tax" className="text-base">
              Tax rate
              <span className="ml-2 text-sm font-normal text-muted-foreground">percent</span>
            </Label>
            <Input
              id="wizard-tax"
              className="h-11 text-base"
              inputMode="decimal"
              placeholder="0"
              aria-invalid={!!errors.tax_rate_percent}
              {...taxField}
            />
            {errors.tax_rate_percent ? (
              <p className="text-caption text-destructive">{errors.tax_rate_percent.message}</p>
            ) : null}
          </div>
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button type="submit" size="lg" disabled={pending} className="h-11 px-8">
          {pending ? (
            <Loader2Icon className="animate-spin" aria-hidden="true" />
          ) : (
            <ArrowRightIcon aria-hidden="true" />
          )}
          {pending ? "Setting up your store…" : "Finish setup"}
        </Button>
      </div>
    </form>
  );
}