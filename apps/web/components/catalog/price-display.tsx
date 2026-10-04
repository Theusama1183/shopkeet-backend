import { formatCurrency, priceFrom } from "@/lib/formats"

import { cn } from "cn"

const SIZE_CLASSES = {
  sm: "text-body",
  md: "text-h3",
  lg: "text-h1",
} as const

export interface PriceDisplayProps {
  amountCents: number
  currency?: string
  /** "From $X" for variant price ranges across the storefront. */
  mode?: "single" | "from"
  size?: keyof typeof SIZE_CLASSES
  className?: string
}

/** Cents → formatted currency. The one component that owns price copy. */
export function PriceDisplay({
  amountCents,
  currency = "USD",
  mode = "single",
  size = "md",
  className,
}: PriceDisplayProps) {
  return (
    <span className={cn("font-semibold tabular-nums text-foreground", SIZE_CLASSES[size], className)}>
      {mode === "from" ? priceFrom(amountCents, currency) : formatCurrency(amountCents, currency)}
    </span>
  )
}