"use client"

import { AlertTriangleIcon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"

export interface ErrorStateProps {
  title?: string
  description?: string
  /** Re-renders the failed section. Never leave a dead screen behind. */
  onRetry?: () => void
  className?: string
}

/** Shown when an entire section fails to load, not for field-level errors. */
export function ErrorState({
  title = "This section failed to load",
  description,
  onRetry,
  className,
}: ErrorStateProps) {
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-center justify-center gap-1 rounded-lg border border-border bg-card px-6 py-12 text-center",
        className
      )}
    >
      <div className="mb-3 flex size-11 items-center justify-center rounded-full bg-destructive/10 text-destructive">
        <AlertTriangleIcon className="size-5" aria-hidden="true" />
      </div>
      <h3 className="text-h3 font-semibold text-foreground">{title}</h3>
      {description ? (
        <p className="max-w-md text-body text-muted-foreground">{description}</p>
      ) : null}
      {onRetry ? (
        <Button variant="outline" onClick={onRetry} className="mt-4">
          Try again
        </Button>
      ) : null}
    </div>
  )
}