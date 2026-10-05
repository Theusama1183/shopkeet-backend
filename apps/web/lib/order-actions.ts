"use server";

import { revalidatePath } from "next/cache";

import { adminMutation } from "@/lib/admin-api";
import { ApiError } from "@/lib/api";

export interface OrderActionResult {
  ok: boolean;
  message?: string;
}

interface OrderResult {
  status?: string;
  payment_status?: string;
}

/** PATCH /orders/:id/status — advance or cancel; tracking rides along on shipped. */
export async function advanceOrderStatusAction(
  orderId: string,
  input: { status?: string; tracking_number?: string; tracking_carrier?: string; tracking_url?: string }
): Promise<OrderActionResult> {
  const body: Record<string, string> = {};
  if (input.status) body.status = input.status;
  if (input.tracking_number) body.tracking_number = input.tracking_number;
  if (input.tracking_carrier) body.tracking_carrier = input.tracking_carrier;
  if (input.tracking_url) body.tracking_url = input.tracking_url;

  try {
    await adminMutation<OrderResult>(`/orders/${orderId}/status`, { method: "PATCH", body });
    revalidatePath(`/admin/orders/${orderId}`);
    return { ok: true };
  } catch (error) {
    if (error instanceof ApiError) return { ok: false, message: error.message };
    return { ok: false, message: "Could not update the order." };
  }
}

/** PATCH /orders/:id/note — the merchant-only internal note. */
export async function saveInternalNoteAction(orderId: string, note: string): Promise<OrderActionResult> {
  try {
    await adminMutation<OrderResult>(`/orders/${orderId}/note`, { method: "PATCH", body: { note } });
    revalidatePath(`/admin/orders/${orderId}`);
    return { ok: true };
  } catch (error) {
    if (error instanceof ApiError) return { ok: false, message: error.message };
    return { ok: false, message: "Could not save the note." };
  }
}

/** POST /orders/draft — the merchant-created phone/WhatsApp sale. */
export async function createDraftOrderAction(input: {
  customer_name: string;
  customer_phone: string;
  customer_email?: string;
  shipping_address_line1: string;
  shipping_address_line2?: string;
  shipping_city: string;
  shipping_state?: string;
  shipping_postal_code?: string;
  shipping_country: string;
  shipping_rate_id: string;
  lines: { variant_id: string; quantity: number; unit_price_cents?: number }[];
}): Promise<{ ok: boolean; message?: string; orderId?: string }> {
  try {
    const result = await adminMutation<{ id: string }>("/orders/draft", { method: "POST", body: input });
    revalidatePath("/admin/orders");
    return { ok: true, orderId: result.id };
  } catch (error) {
    if (error instanceof ApiError) return { ok: false, message: error.message };
    return { ok: false, message: "Could not create the draft." };
  }
}