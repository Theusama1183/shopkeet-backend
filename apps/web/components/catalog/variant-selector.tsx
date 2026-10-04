"use client"

import { cn } from "cn"
import { Button } from "@/components/ui/button"

export interface VariantOption {
  id: string
  label: string
  disabled?: boolean
}

export interface VariantSelectorProps {
  label?: string
  options: VariantOption[]
  selectedId?: string | null
  onSelect: (id: string) => void
  className?: string
}

/** Segmented option picker for product variants. */
export function VariantSelector({
  label,
  options,
  selectedId,
  onSelect,
  className,
}: VariantSelectorProps) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      {label ? (
        <span className="text-label font-medium text-foreground">{label}</span>
      ) : null}
      <div className="flex flex-wrap gap-1.5" role="group" aria-label={label ?? "Options"}>
        {options.map((option) => (
          <Button
            key={option.id}
            type="button"
            size="sm"
            variant={option.id === selectedId ? "default" : "outline"}
            disabled={option.disabled}
            aria-pressed={option.id === selectedId}
            onClick={() => onSelect(option.id)}
            className="min-w-11"
          >
            {option.label}
          </Button>
        ))}
      </div>
    </div>
  )
}