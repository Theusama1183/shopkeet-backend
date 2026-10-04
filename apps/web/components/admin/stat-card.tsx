import type { LucideIcon } from "lucide-react"

import { cn } from "cn"
import { Card, CardContent } from "@/components/ui/card"

export interface StatCardTrend {
  direction: "up" | "down"
  label: string
  tone?: "success" | "danger" | "neutral"
}

export interface StatCardProps {
  label: string
  value: string
  hint?: string
  trend?: StatCardTrend
  icon?: LucideIcon
  className?: string
}

const TREND_TONES: Record<NonNullable<StatCardTrend["tone"]>, string> = {
  success: "text-success",
  danger: "text-destructive",
  neutral: "text-muted-foreground",
}

/** A single dashboard number with a label and optional trend — Phase 24 home. */
export function StatCard({ label, value, hint, trend, icon: Icon, className }: StatCardProps) {
  const trendTone = trend?.tone ?? "neutral"
  return (
    <Card className={cn("gap-0", className)}>
      <CardContent className="p-4">
        <div className="flex items-center justify-between gap-2">
          <span className="text-label font-medium text-muted-foreground">{label}</span>
          {Icon ? <Icon className="size-4 text-muted-foreground" aria-hidden="true" /> : null}
        </div>
        <div className="mt-1.5 text-h2 font-semibold tracking-tight tabular-nums text-foreground">
          {value}
        </div>
        <div className="mt-1 flex items-center gap-2 text-caption">
          {trend ? (
            <span className={cn("font-medium", TREND_TONES[trendTone])}>
              {trend.direction === "up" ? "↑" : "↓"} {trend.label}
            </span>
          ) : null}
          {hint ? <span className="text-muted-foreground">{hint}</span> : null}
        </div>
      </CardContent>
    </Card>
  )
}