import { Loader2Icon } from "lucide-react"

import { cn } from "cn"

/**
 * Reserved for small inline affordances — a button's own pending state, a
 * loading indicator inside a cell. Full-section loading uses Skeleton, not
 * this (docs/05, 11-frontend-foundation-build-spec Tier 3).
 */
export function Spinner({
  className,
  label,
}: {
  className?: string
  label?: string
}) {
  return (
    <span
      role="status"
      className={cn("inline-flex items-center gap-1.5 text-muted-foreground", className)}
    >
      <Loader2Icon className="size-4 animate-spin" aria-hidden="true" />
      {label ? (
        <span className="text-label">{label}</span>
      ) : (
        <span className="sr-only">Loading</span>
      )}
    </span>
  )
}