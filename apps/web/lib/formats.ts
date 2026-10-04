import { format } from "date-fns"

const CURRENCY_LOCALE = "en-US"

/** Formats an integer amount in the minor unit of the currency (cents). */
export function formatCurrency(cents: number, currency = "USD"): string {
  const minorFraction = cents % 100 === 0 ? 0 : 2
  return new Intl.NumberFormat(CURRENCY_LOCALE, {
    style: "currency",
    currency,
    minimumFractionDigits: minorFraction,
  }).format(cents / 100)
}

/** "From $12.00" style range copy for variant pricing, per Tier 5. */
export function priceFrom(cents: number, currency = "USD"): string {
  return `From ${formatCurrency(cents, currency)}`
}

/** Compact short date, e.g. "Sep 4, 2026". */
export function formatShortDate(value: Date | string): string {
  const date = typeof value === "string" ? new Date(value) : value
  return format(date, "MMM d, yyyy")
}