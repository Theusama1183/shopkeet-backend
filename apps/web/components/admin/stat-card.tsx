import { cn } from "cn"

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
  className?: string
}

const TREND_TONES: Record<NonNullable<StatCardTrend["tone"]>, string> = {
  success: "text-success",
  danger: "text-destructive",
  neutral: "text-muted-foreground",
}

/**
 * A compact secondary metric sitting directly on the canvas — no card box, no
 * decorative icon. Number size and weight carry the hierarchy, so a screen can
 * pair one large hero figure with a cluster of these without four identical
 * boxes competing for attention (docs/05: decide the hierarchy first).
 */
export function StatCard({ label, value, hint, trend, className }: StatCardProps) {
  const trendTone = trend?.tone ?? "neutral"
  return (
    <div className={cn("min-w-0", className)}>
      <p className="text-label font-medium text-muted-foreground">{label}</p>
      <p className="mt-1 truncate text-h2 font-semibold tracking-tight tabular-nums text-foreground">
        {value}
      </p>
      {trend || hint ? (
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption">
          {trend ? (
            <span className={cn("font-medium", TREND_TONES[trendTone])}>
              {trend.direction === "up" ? "↑" : "↓"} {trend.label}
            </span>
          ) : null}
          {hint ? <span className="text-muted-foreground">{hint}</span> : null}
        </div>
      ) : null}
    </div>
  )
}
