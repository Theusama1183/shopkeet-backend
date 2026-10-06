import { cn } from "cn"
import { Badge } from "@/components/ui/badge"

export type StatusTone = "neutral" | "success" | "warning" | "danger" | "info"

export interface StatusBadgeProps {
  tone?: StatusTone
  children: React.ReactNode
  className?: string
}

const TONE_CLASSES: Record<StatusTone, string> = {
  neutral: "border-transparent bg-secondary text-secondary-foreground",
  success: "border-transparent bg-badge-success-bg text-badge-success-fg",
  warning: "border-transparent bg-badge-warning-bg text-badge-warning-fg",
  danger: "border-transparent bg-badge-danger-bg text-badge-danger-fg",
  info: "border-transparent bg-primary/15 text-primary",
}

/**
 * Order/payment/affiliate status pill — solid Polaris-style tint pairs.
 * One consistent tone mapping, defined once, reused everywhere a status
 * appears; the text carries the meaning, so no decorative dot.
 */
export function StatusBadge({ tone = "neutral", children, className }: StatusBadgeProps) {
  return (
    <Badge variant="outline" className={cn("font-medium", TONE_CLASSES[tone], className)}>
      {children}
    </Badge>
  )
}