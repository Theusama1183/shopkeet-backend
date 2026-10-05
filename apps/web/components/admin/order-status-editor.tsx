"use client";

import * as React from "react";
import { useActionState } from "react";

import { advanceOrderStatusAction, saveInternalNoteAction, type OrderActionResult } from "@/lib/order-actions";
import { orderTone, label, ORDER_LABELS } from "@/lib/statuses";
import { StatusBadge } from "@/components/admin/status-badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Alert, AlertDescription } from "@/components/ui/alert";

/**
 * Current status + the next step. Uses a server action bound to the order; the
 * action revalidates this route so the freshly-updated order re-renders.
 */
export function OrderStatusEditor({
  orderId,
  status,
  paymentStatus,
  trackingNumber,
  trackingCarrier,
  trackingUrl,
}: {
  orderId: string;
  status: string;
  paymentStatus: string;
  trackingNumber: string | null;
  trackingCarrier: string | null;
  trackingUrl: string | null;
}) {
  const canCancel = status === "pending" || status === "confirmed";
  const next = { pending: "confirmed", confirmed: "shipped", shipped: "delivered" }[status];

  const [state, formAction, isPending] = useActionState<OrderActionResult, FormData>(
    async (_prev: OrderActionResult, formData: FormData) => advanceOrderStatusAction(orderId, {
      status: (formData.get("status") as string) || undefined,
      tracking_number: (formData.get("tracking_number") as string) || undefined,
      tracking_carrier: (formData.get("tracking_carrier") as string) || undefined,
      tracking_url: (formData.get("tracking_url") as string) || undefined,
    }),
    { ok: true }
  );

  const showTracking = status === "confirmed" || status === "shipped";

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge tone={orderTone(status)}>{label(ORDER_LABELS, status)}</StatusBadge>
        <span className="text-caption text-muted-foreground">
          Payment: {paymentStatus}
        </span>
      </div>

      {state && !state.ok && state.message ? (
        <Alert variant="destructive">
          <AlertDescription>{state.message}</AlertDescription>
        </Alert>
      ) : null}

      <form action={formAction}>
        {showTracking ? (
          <div className="space-y-3">
            <Separator />
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="tracking_number">Tracking number</Label>
                <Input id="tracking_number" name="tracking_number" defaultValue={trackingNumber ?? ""} placeholder="e.g. 1Z999AA10123456784" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="tracking_carrier">Carrier</Label>
                <Input id="tracking_carrier" name="tracking_carrier" defaultValue={trackingCarrier ?? ""} placeholder="e.g. FedEx" />
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="tracking_url">Tracking URL</Label>
              <Input id="tracking_url" name="tracking_url" defaultValue={trackingUrl ?? ""} placeholder="https://…" />
            </div>
          </div>
        ) : null}

        <div className="mt-4 flex flex-wrap gap-2">
          {next ? (
            <Button type="submit" size="sm" name="status" value={next} disabled={isPending}>
              {isPending ? "Updating…" : `Mark ${next}`}
            </Button>
          ) : null}
          {canCancel ? (
            <Button
              type="submit"
              size="sm"
              variant="destructive"
              name="status"
              value="cancelled"
              disabled={isPending}
            >
              Cancel order
            </Button>
          ) : null}
        </div>
      </form>
    </div>
  );
}

/** Merchant-only internal note editor. */
export function OrderNoteEditor({
  orderId,
  note,
}: {
  orderId: string;
  note: string | null;
}) {
  const [state, formAction, isPending] = useActionState<OrderActionResult, FormData>(
    async (_prev: OrderActionResult, formData: FormData) =>
      saveInternalNoteAction(orderId, (formData.get("note") as string) ?? ""),
    { ok: true }
  );

  return (
    <form action={formAction} className="space-y-3">
      <div className="grid gap-1.5">
        <Label htmlFor="note">Internal note</Label>
        <Textarea
          id="note"
          name="note"
          rows={4}
          defaultValue={note ?? ""}
          placeholder="Visible only to you — e.g. 'call before dispatch'"
        />
      </div>
      {state && !state.ok && state.message ? (
        <p className="text-sm text-destructive">{state.message}</p>
      ) : null}
      <Button type="submit" size="sm" disabled={isPending}>
        {isPending ? "Saving…" : "Save note"}
      </Button>
    </form>
  );
}