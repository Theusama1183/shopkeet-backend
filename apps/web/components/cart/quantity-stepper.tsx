"use client"

import { MinusIcon, PlusIcon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

export interface QuantityStepperProps {
  value: number
  onChange: (value: number) => void
  min?: number
  max?: number
  label?: string
  className?: string
}

/** Compact +/- stepper; the non-trusting math lives in handlers, here it clamps. */
export function QuantityStepper({
  value,
  onChange,
  min = 1,
  max = 999,
  label,
  className,
}: QuantityStepperProps) {
  const clamp = (next: number) => Math.min(max, Math.max(min, next))

  return (
    <div className={cn("inline-flex items-center gap-1", className)}>
      <Button
        type="button"
        variant="outline"
        size="icon-sm"
        onClick={() => onChange(clamp(value - 1))}
        disabled={value <= min}
        aria-label={`Decrease quantity${label ? ` of ${label}` : ""}`}
      >
        <MinusIcon />
      </Button>
      <Input
        type="number"
        value={value}
        min={min}
        max={max}
        inputMode="numeric"
        aria-label={label ?? "Quantity"}
        onChange={(event) => {
          const next = Number(event.target.value)
          if (!Number.isNaN(next)) onChange(clamp(next))
        }}
        className="h-7 w-12 text-center tabular-nums"
      />
      <Button
        type="button"
        variant="outline"
        size="icon-sm"
        onClick={() => onChange(clamp(value + 1))}
        disabled={value >= max}
        aria-label={`Increase quantity${label ? ` of ${label}` : ""}`}
      >
        <PlusIcon />
      </Button>
    </div>
  )
}