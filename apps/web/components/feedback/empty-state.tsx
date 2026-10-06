import type { LucideIcon } from "lucide-react"
import { InboxIcon } from "lucide-react"

import { cn } from "cn"

export interface EmptyStateProps {
  icon?: LucideIcon
  title: string
  description?: string
  action?: React.ReactNode
  className?: string
}

/**
 * Every list screen's empty case. "No data" with nothing else to do is not an
 * acceptable empty state anywhere in the app — say what to do next.
 */
export function EmptyState({
  icon: Icon = InboxIcon,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-1 px-6 py-12 text-center",
        className
      )}
    >
      <div className="mb-3 flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <Icon className="size-5" aria-hidden="true" />
      </div>
      <h3 className="text-h3 font-semibold text-foreground">{title}</h3>
      {description ? (
        <p className="max-w-sm text-body text-muted-foreground">{description}</p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  )
}