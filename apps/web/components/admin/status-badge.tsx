import { cn } from "cn"
import { Badge } from "@/components/ui/badge"

export type StatusTone = "neutral" | "success" | "warning" | "danger" | "info"

export interface StatusBadgeProps {
  tone?: StatusTone
  children: React.ReactNode
  className?: string
}

const TONE_CLASSES: Record<StatusTone, string> = {
  neutral: "bg-secondary text-secondary-foreground",
  success: "bg-success/10 text-success",
  warning: "bg-warning/10 text-warning",
  danger: "bg-destructive/10 text-destructive",
  info: "bg-primary/10 text-primary",
}

const DOT_CLASSES: Record<StatusTone, string> = {
  neutral: "bg-muted-foreground",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  info: "bg-primary",
}

/**
 * Order/payment/affiliate status pill. One consistent tone mapping, defined
 * once, reused everywhere a status appears.
 */
export function StatusBadge({ tone = "neutral", children, className }: StatusBadgeProps) {
  return (
    <Badge variant="outline" className={cn("gap-1.5 font-medium", TONE_CLASSES[tone], className)}>
      <span className={cn("size-1.5 rounded-full", DOT_CLASSES[tone])} aria-hidden="true" />
      {children}
    </Badge>
  )
}