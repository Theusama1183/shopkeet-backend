"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2Icon, PlusIcon, Trash2Icon } from "lucide-react";

import { formatMoney } from "@/lib/format";
import type { AdminProduct, ShippingRate } from "@/lib/admin-types";
import { createDraftOrderAction } from "@/lib/order-actions";
import { apiErrorToast } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const draftSchema = z
  .object({
    customer_name: z.string().trim().min(1, "Enter the customer's name.").max(120),
    customer_phone: z.string().trim().min(1, "Enter a phone number."),
    customer_email: z.string().trim().email("Enter a valid email.").optional().or(z.literal("")),
    shipping_address_line1: z.string().trim().min(1, "Enter a street address."),
    shipping_address_line2: z.string().trim().optional().or(z.literal("")),
    shipping_city: z.string().trim().min(1, "Enter a city."),
    shipping_state: z.string().trim().optional().or(z.literal("")),
    shipping_postal_code: z.string().trim().optional().or(z.literal("")),
    shipping_country: z.string().trim().min(2, "Enter a country code, e.g. US."),
    shipping_rate_id: z.string().min(1, "Choose a shipping option."),
  });

type DraftValues = z.infer<typeof draftSchema>;

interface DraftLine {
  product_id: string;
  variant_id: string;
  quantity: number;
}

export function NewDraftForm({
  products,
  rates,
  stateRequired,
}: {
  products: AdminProduct[];
  rates: ShippingRate[];
  stateRequired: boolean;
}) {
  const router = useRouter();
  const [pending, setPending] = React.useState(false);
  const [rootError, setRootError] = React.useState<string | null>(null);
  const [lines, setLines] = React.useState<DraftLine[]>([{ product_id: "", variant_id: "", quantity: 1 }]);
  const [lineError, setLineError] = React.useState<string | null>(null);

  const schema = React.useMemo(() => {
    if (!stateRequired) return draftSchema;
    return draftSchema.extend({
      shipping_state: z.string().trim().min(1, "This zone requires a state."),
    });
  }, [stateRequired]);

  const {
    register,
    control,
    handleSubmit,
    formState: { errors },
  } = useForm<DraftValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      customer_name: "",
      customer_phone: "",
      customer_email: "",
      shipping_address_line1: "",
      shipping_address_line2: "",
      shipping_city: "",
      shipping_state: "",
      shipping_postal_code: "",
      shipping_country: "US",
      shipping_rate_id: "",
    },
  });

  const setLine = (index: number, patch: Partial<DraftLine>) => {
    setLines((prev) => prev.map((line, i) => (i === index ? { ...line, ...patch } : line)));
    setLineError(null);
  };

  const variantFor = (line: DraftLine) => {
    const product = products.find((p) => p.id === line.product_id);
    return product?.variants.find((v) => v.id === line.variant_id);
  };

  async function onSubmit(values: DraftValues) {
    const validLines = lines.every(
      (line) =>
        line.product_id &&
        line.variant_id &&
        line.quantity >= 1 &&
        (variantFor(line)?.inventory_count ?? 0) >= line.quantity
    );
    if (!validLines) {
      setLineError("Check the line items — each needs a variant and a quantity that's in stock.");
      return;
    }

    setPending(true);
    setRootError(null);
    try {
      const result = await createDraftOrderAction({
        customer_name: values.customer_name,
        customer_phone: values.customer_phone,
        customer_email: values.customer_email || undefined,
        shipping_address_line1: values.shipping_address_line1,
        shipping_address_line2: values.shipping_address_line2 || undefined,
        shipping_city: values.shipping_city,
        shipping_state: values.shipping_state || undefined,
        shipping_postal_code: values.shipping_postal_code || undefined,
        shipping_country: values.shipping_country,
        shipping_rate_id: values.shipping_rate_id,
        lines: lines.map((line) => ({ variant_id: line.variant_id, quantity: line.quantity })),
      });
      if (!result.ok) {
        setRootError(result.message ?? "Could not create the draft.");
        setPending(false);
        return;
      }
      router.push(`/admin/orders/${result.orderId}`);
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
      setPending(false);
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="grid gap-4" noValidate>
      {rootError ? (
        <Alert variant="destructive">
          <AlertDescription>{rootError}</AlertDescription>
        </Alert>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Customer</CardTitle>
          <CardDescription>Who is buying?</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="draft-name">Name</Label>
            <Input id="draft-name" autoComplete="name" placeholder="Ali Raza" aria-invalid={!!errors.customer_name} {...register("customer_name")} />
            {errors.customer_name ? <p className="text-caption text-destructive">{errors.customer_name.message}</p> : null}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="draft-phone">Phone</Label>
            <Input id="draft-phone" type="tel" autoComplete="tel" placeholder="+92 300 1234567" aria-invalid={!!errors.customer_phone} {...register("customer_phone")} />
            {errors.customer_phone ? <p className="text-caption text-destructive">{errors.customer_phone.message}</p> : null}
          </div>
          <div className="grid gap-2 sm:col-span-2">
            <Label htmlFor="draft-email">Email (optional)</Label>
            <Input id="draft-email" type="email" autoComplete="email" placeholder="ali@example.com" aria-invalid={!!errors.customer_email} {...register("customer_email")} />
            {errors.customer_email ? <p className="text-caption text-destructive">{errors.customer_email.message}</p> : null}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Shipping address</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2 sm:col-span-2">
            <Label htmlFor="draft-address1">Address</Label>
            <Input id="draft-address1" autoComplete="address-line1" placeholder="House 12, Street 9" aria-invalid={!!errors.shipping_address_line1} {...register("shipping_address_line1")} />
            {errors.shipping_address_line1 ? <p className="text-caption text-destructive">{errors.shipping_address_line1.message}</p> : null}
          </div>
          <div className="grid gap-2 sm:col-span-2">
            <Label htmlFor="draft-address2">Apartment, suite, etc. (optional)</Label>
            <Input id="draft-address2" autoComplete="address-line2" aria-invalid={!!errors.shipping_address_line2} {...register("shipping_address_line2")} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="draft-city">City</Label>
            <Input id="draft-city" autoComplete="address-level2" aria-invalid={!!errors.shipping_city} {...register("shipping_city")} />
            {errors.shipping_city ? <p className="text-caption text-destructive">{errors.shipping_city.message}</p> : null}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="draft-state">State / region{stateRequired ? " *" : ""}</Label>
            <Input id="draft-state" autoComplete="address-level1" aria-invalid={!!errors.shipping_state} {...register("shipping_state")} />
            {errors.shipping_state ? <p className="text-caption text-destructive">{errors.shipping_state.message}</p> : null}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="draft-postal">Postal code</Label>
            <Input id="draft-postal" autoComplete="postal-code" aria-invalid={!!errors.shipping_postal_code} {...register("shipping_postal_code")} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="draft-country">Country code</Label>
            <Input id="draft-country" autoComplete="country-name" placeholder="US" aria-invalid={!!errors.shipping_country} {...register("shipping_country")} />
            {errors.shipping_country ? <p className="text-caption text-destructive">{errors.shipping_country.message}</p> : null}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Items</CardTitle>
          <CardDescription>What&apos;s in the order?</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {lineError ? (
            <Alert variant="destructive">
              <AlertDescription>{lineError}</AlertDescription>
            </Alert>
          ) : null}

          <div className="grid gap-3">
            {lines.map((line, index) => {
              const product = products.find((p) => p.id === line.product_id);
              const variants = product?.variants.filter((v) => v.inventory_count > 0) ?? [];
              const variant = variantFor(line);
              return (
                <div key={index} className="grid items-end gap-3 rounded-lg border border-border p-3 sm:grid-cols-12">
                  <div className="grid gap-1.5 sm:col-span-5">
                    <Label htmlFor={`draft-line-${index}-product`}>Product</Label>
                    <Select
                      value={line.product_id || undefined}
                      onValueChange={(product_id) => setLine(index, { product_id, variant_id: "" })}
                    >
                      <SelectTrigger id={`draft-line-${index}-product`} size="sm" aria-label="Product">
                        <SelectValue placeholder="Choose a product" />
                      </SelectTrigger>
                      <SelectContent>
                        {products.map((p) => (
                          <SelectItem key={p.id} value={p.id}>
                            {p.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-1.5 sm:col-span-4">
                    <Label htmlFor={`draft-line-${index}-variant`}>Variant</Label>
                    <Select
                      value={line.variant_id || undefined}
                      onValueChange={(variant_id) => setLine(index, { variant_id })}
                      disabled={!product}
                    >
                      <SelectTrigger id={`draft-line-${index}-variant`} size="sm" aria-label="Variant">
                        <SelectValue placeholder={product ? "Choose a variant" : "Pick a product first"} />
                      </SelectTrigger>
                      <SelectContent>
                        {variants.map((v) => {
                          const label = [v.sku, ...v.option_values.map((o) => `${o.option_name}: ${o.value}`)]
                            .filter(Boolean)
                            .join(", ");
                          return (
                            <SelectItem key={v.id} value={v.id}>
                              {label}, {v.inventory_count} in stock
                            </SelectItem>
                          );
                        })}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-1.5 sm:col-span-2">
                    <Label htmlFor={`draft-line-${index}-quantity`}>Qty</Label>
                    <Input
                      id={`draft-line-${index}-quantity`}
                      type="number"
                      min={1}
                      value={line.quantity}
                      onChange={(event) => setLine(index, { quantity: Number(event.target.value) })}
                      aria-label="Quantity"
                    />
                  </div>
                  <div className="flex items-center justify-between gap-2 sm:col-span-1 sm:block">
                    <p className="text-caption text-muted-foreground">
                      {variant ? formatMoney(variant.price_cents) : "—"}
                    </p>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() => {
                        setLines((prev) => prev.filter((_, i) => i !== index));
                        setLineError(null);
                      }}
                      disabled={lines.length === 1}
                      aria-label="Remove item"
                    >
                      <Trash2Icon className="h-4 w-4" aria-hidden="true" />
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>

          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => setLines((prev) => [...prev, { product_id: "", variant_id: "", quantity: 1 }])}
          >
            <PlusIcon aria-hidden="true" />
            Add item
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Shipping</CardTitle>
          <CardDescription>How will it ship?</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2">
          <Controller
            control={control}
            name="shipping_rate_id"
            render={({ field }) => (
              <div className="grid gap-1.5">
                <Label htmlFor="draft-rate">Shipping method</Label>
                <Select value={field.value || undefined} onValueChange={field.onChange}>
                  <SelectTrigger id="draft-rate" className="w-full" aria-invalid={!!errors.shipping_rate_id}>
                    <SelectValue placeholder="Choose a shipping method" />
                  </SelectTrigger>
                  <SelectContent>
                    {rates.map((rate) => (
                      <SelectItem key={rate.id} value={rate.id}>
                        {rate.name} ({formatMoney(rate.rate_cents)})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {errors.shipping_rate_id ? (
                  <p className="text-caption text-destructive">{errors.shipping_rate_id.message}</p>
                ) : null}
              </div>
            )}
          />
        </CardContent>
      </Card>

      <Button type="submit" className="w-fit" disabled={pending}>
        {pending ? <Loader2Icon className="animate-spin" aria-hidden="true" /> : null}
        {pending ? "Creating order…" : "Create draft order"}
      </Button>
    </form>
  );
}