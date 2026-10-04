import Image from "next/image"

import { cn } from "cn"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { PriceDisplay } from "@/components/catalog/price-display"

export interface ProductCardImage {
  src: string
  alt: string
  width?: number
  height?: number
}

export interface ProductCardProps {
  name: string
  amountCents: number
  currency?: string
  image?: ProductCardImage | null
  badge?: string
  /** e.g. "Sand — Small" when a default variant is selected. */
  variantLabel?: string
  /** Quick-add lands here; the card itself stays presentational. */
  action?: React.ReactNode
  className?: string
}

/**
 * Product-facing cards let the product image and name carry the design —
 * deliberately light chrome (docs/05 Storefront specifics).
 */
export function ProductCard({
  name,
  amountCents,
  currency = "USD",
  image,
  badge,
  variantLabel,
  action,
  className,
}: ProductCardProps) {
  return (
    <Card
      className={cn(
        "gap-0 overflow-hidden rounded-md border-border transition-colors hover:border-foreground/20",
        className
      )}
    >
      <div className="relative aspect-[4/5] w-full overflow-hidden bg-muted">
        {image ? (
          <Image
            src={image.src}
            alt={image.alt}
            width={image.width ?? 640}
            height={image.height ?? 800}
            unoptimized={image.src.startsWith("data:") ? true : undefined}
            className="size-full object-cover"
          />
        ) : (
          <div className="flex size-full items-center justify-center text-caption text-muted-foreground">
            No image
          </div>
        )}
        {badge ? (
          <Badge className="absolute top-2 left-2 bg-popover/90 text-popover-foreground backdrop-blur">
            {badge}
          </Badge>
        ) : null}
      </div>
      <div className="flex flex-col gap-1 p-3">
        <div className="flex items-start justify-between gap-2">
          <h3 className="line-clamp-2 text-body font-medium text-foreground">{name}</h3>
          {action}
        </div>
        <div className="flex items-baseline gap-2">
          <PriceDisplay amountCents={amountCents} currency={currency} size="sm" />
          {variantLabel ? (
            <span className="truncate text-caption text-muted-foreground">{variantLabel}</span>
          ) : null}
        </div>
      </div>
    </Card>
  )
}