import type { StatusTone } from "@/components/admin/status-badge";

/**
 * The status vocabularies the Go API actually emits, mapped to the admin badge
 * tones. Kept apart from the API types so pages stay dumb about colors.
 */

export const ORDER_TONES: Record<string, StatusTone> = {
  pending: "warning",
  confirmed: "info",
  shipped: "success",
  delivered: "success",
  cancelled: "danger",
};

export const PAYMENT_TONES: Record<string, StatusTone> = {
  pending: "warning",
  paid: "success",
  refunded: "info",
  failed: "danger",
};

export const ORDER_LABELS: Record<string, string> = {
  pending: "Pending",
  confirmed: "Confirmed",
  shipped: "Shipped",
  delivered: "Delivered",
  cancelled: "Cancelled",
};

export const PAYMENT_LABELS: Record<string, string> = {
  pending: "Unpaid",
  paid: "Paid",
  refunded: "Refunded",
  failed: "Failed",
};

export function label(labels: Record<string, string>, value: string): string {
  return labels[value] ?? value;
}

export function orderTone(status: string): StatusTone {
  return ORDER_TONES[status] ?? "neutral";
}

export function paymentTone(status: string): StatusTone {
  return PAYMENT_TONES[status] ?? "neutral";
}