"use client"

import Image from "next/image"
import { Trash2Icon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"
import { PriceDisplay } from "@/components/catalog/price-display"
import { QuantityStepper } from "@/components/cart/quantity-stepper"
import { formatCurrency } from "@/lib/formats"

export interface CartLineImage {
  src: string
  alt: string
}

export interface CartLineItemProps {
  name: string
  variantLabel?: string
  image?: CartLineImage | null
  unitAmountCents: number
  currency?: string
  quantity: number
  onQuantityChange: (quantity: number) => void
  onRemove: () => void
  editable?: boolean
  className?: string
}

/** One cart row: thumbnail, name/variant, editing, line total. */
export function CartLineItem({
  name,
  variantLabel,
  image,
  unitAmountCents,
  currency = "USD",
  quantity,
  onQuantityChange,
  onRemove,
  editable = true,
  className,
}: CartLineItemProps) {
  const lineTotal = unitAmountCents * quantity
  return (
    <div
      className={cn("flex items-start gap-4 py-4", className)}
    >
      <div className="size-20 shrink-0 overflow-hidden rounded-md bg-muted">
        {image ? (
          <Image
            src={image.src}
            alt={image.alt}
            width={160}
            height={160}
            unoptimized={image.src.startsWith("data:") ? true : undefined}
            className="size-full object-cover"
          />
        ) : null}
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="truncate text-body font-medium text-foreground">{name}</p>
            {variantLabel ? (
              <p className="truncate text-caption text-muted-foreground">{variantLabel}</p>
            ) : null}
          </div>
          <PriceDisplay amountCents={lineTotal} currency={currency} size="sm" />
        </div>
        <div className="mt-1 flex items-center justify-between gap-2">
          <span className="text-caption text-muted-foreground">
            {formatCurrency(unitAmountCents, currency)} each
          </span>
          <div className="flex items-center gap-2">
            {editable ? (
              <QuantityStepper value={quantity} onChange={onQuantityChange} label={name} />
            ) : (
              <span className="text-label text-muted-foreground">× {quantity}</span>
            )}
            {editable ? (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={onRemove}
                aria-label={`Remove ${name} from cart`}
              >
                <Trash2Icon />
              </Button>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}