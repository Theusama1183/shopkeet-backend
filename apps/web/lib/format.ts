/**
 * Client-safe money/date formats for the admin surface. Every amount in the Go
 * API is integer minor units (cents) plus an ISO-4217 currency code, so we
 * never format from floats.
 */

export function formatMoney(cents: number, currency = "usd"): string {
  const code = (currency ?? "usd").toUpperCase();
  const value = cents / 100;
  try {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: code,
      currencyDisplay: "narrowSymbol",
    }).format(value);
  } catch {
    return `${value.toFixed(2)} ${code}`;
  }
}

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", year: "numeric" }).format(date);
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
}

export function formatPercent(rate: number): string {
  return `${(rate * 100).toFixed(2)}%`;
}