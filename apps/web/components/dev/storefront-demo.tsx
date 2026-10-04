"use client"

import { useState } from "react"
import { MoonIcon, SunIcon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { ProductCard } from "@/components/catalog/product-card"
import { PriceDisplay } from "@/components/catalog/price-display"
import { VariantSelector } from "@/components/catalog/variant-selector"
import { QuantityStepper } from "@/components/cart/quantity-stepper"
import { CartLineItem } from "@/components/cart/cart-line-item"
import { MediaPicker } from "@/components/catalog/media-picker"
import { sampleImage } from "@/components/dev/sample-image"
import { storefrontThemeVars } from "@/lib/storefront-theme"

const DEFAULT_BRAND = "#1f5f55"
const FALLBACK_BRAND = "#a4513c"

const CART_LINES = [
  {
    id: "c1",
    name: "Stoneware mug — Sand",
    unitAmountCents: 2400,
    qty: 2,
    image: { src: sampleImage(150), alt: "Stoneware mug in sand clay" },
  },
  {
    id: "c2",
    name: "Linen tea towel — Ochre",
    unitAmountCents: 1800,
    qty: 1,
    image: { src: sampleImage(200), alt: "Linen tea towel in ochre" },
  },
  {
    id: "c3",
    name: "Wax seal set",
    unitAmountCents: 1450,
    qty: 3,
    image: { src: sampleImage(30), alt: "Wax seal set" },
  },
]

const MEDIA = [
  { id: "m1", url: sampleImage(150), alt: "Mug photo" },
  { id: "m2", url: sampleImage(200), alt: "Towel photo" },
  { id: "m3", url: sampleImage(30), alt: "Seal photo" },
  { id: "m4", url: sampleImage(95), alt: "Bowl photo" },
  { id: "m5", url: sampleImage(250), alt: "Candle photo" },
  { id: "m6", url: sampleImage(40), alt: "Notebook photo" },
]

export function StorefrontDemo() {
  const [mode, setMode] = useState<"light" | "dark">("light")
  const [customBrand, setCustomBrand] = useState(false)
  const [brand, setBrand] = useState(DEFAULT_BRAND)
  const [variant, setVariant] = useState("v-sand")
  const [qty, setQty] = useState(2)
  const [mediaValue, setMediaValue] = useState<string | null>("m1")
  const [lineQty, setLineQty] = useState<Record<string, number>>(() =>
    Object.fromEntries(CART_LINES.map((line) => [line.id, line.qty]))
  )

  const activeBrand = customBrand ? brand : ""
  const vars = storefrontThemeVars({ brand: activeBrand }, mode)
  const showFallback = !customBrand

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-2 rounded-lg border border-border bg-card p-4">
        <div className="flex items-center gap-1">
          {(["light", "dark"] as const).map((m) => (
            <Button
              key={m}
              type="button"
              size="sm"
              variant={mode === m ? "secondary" : "ghost"}
              onClick={() => setMode(m)}
              className="capitalize"
            >
              {m === "dark" ? <MoonIcon className="size-3.5" /> : <SunIcon className="size-3.5" />}
              {m}
            </Button>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor="sf-custom-brand" className="text-muted-foreground">
            Custom brand
          </Label>
          <Switch id="sf-custom-brand" checked={customBrand} onCheckedChange={setCustomBrand} />
        </div>
        {customBrand ? (
          <div className="flex items-center gap-2">
            <Input
              type="color"
              value={brand}
              onChange={(event) => setBrand(event.target.value)}
              aria-label="Brand color"
              className="h-9 w-12 cursor-pointer p-1"
            />
            <Input
              value={brand}
              onChange={(event) => setBrand(event.target.value)}
              aria-label="Brand color (hex)"
              className="h-9 w-28 font-mono text-caption"
            />
            <Badge variant="outline" className="font-mono">
              {Object.entries(vars).map(([key]) => key).join(", ") || "fallback tokens"}
            </Badge>
          </div>
        ) : (
          <Button type="button" variant="outline" size="sm" onClick={() => setBrand(FALLBACK_BRAND)}>
            Set to {FALLBACK_BRAND.toUpperCase()}
          </Button>
        )}
      </div>

      <div
        data-storefront-root="true"
        style={vars as React.CSSProperties}
        className={cn(
          mode === "dark" && "dark",
          "bg-sf-background text-sf-foreground"
        )}
      >
        <div className="flex flex-col gap-4 border border-sf-border p-4 sm:p-6">
          <h3 className="text-h3 font-semibold text-sf-foreground">North Star Ceramics</h3>

          {showFallback ? (
            <p className="text-caption text-sf-muted-foreground">
              No brand overrides — the storefront is running the default sf token set.
            </p>
          ) : (
            <p className="text-caption text-sf-muted-foreground">
              Live override — <code className="font-mono">{activeBrand}</code> drives every
              storefront utility below via the <code className="font-mono">--sf-*</code> variables.
            </p>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded-md bg-sf-brand px-3 py-1.5 text-label font-semibold text-sf-brand-foreground">
              On brand
            </span>
            <span className="rounded-md bg-sf-brand-soft px-3 py-1.5 text-label font-medium text-sf-brand-foreground">
              Soft tint
            </span>
            <button
              type="button"
              className="rounded-md bg-sf-accent px-4 py-2 text-label font-semibold text-sf-accent-foreground transition-opacity hover:opacity-90"
            >
              Add to cart
            </button>
            <button
              type="button"
              className="rounded-md border border-sf-border px-4 py-2 text-label font-medium text-sf-foreground transition-colors hover:bg-sf-brand-soft"
            >
              Details
            </button>
          </div>

          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <ProductCard
              name="Stoneware mug"
              amountCents={2400}
              image={{ src: sampleImage(150), alt: "Stoneware mug" }}
              badge="Low stock"
              action={<Button size="sm">Add</Button>}
            />
            <ProductCard
              name="Linen tea towel"
              amountCents={1800}
              image={{ src: sampleImage(200), alt: "Linen tea towel" }}
              variantLabel="Ochre"
              action={<Button size="sm">Add</Button>}
            />
            <ProductCard
              name="Ceramic pouring bowl"
              amountCents={3400}
              image={{ src: sampleImage(95), alt: "Ceramic pouring bowl" }}
              action={<Button size="sm">Add</Button>}
            />
            <ProductCard
              name="Wax seal set"
              amountCents={1450}
              image={{ src: sampleImage(30), alt: "Wax seal set" }}
              badge="New"
              action={<Button size="sm">Add</Button>}
            />
          </div>

          <div className="grid gap-4 md:grid-cols-2">
            <div className="flex flex-col gap-3 rounded-md border border-sf-border bg-sf-surface p-4">
              <PriceDisplay amountCents={2400} size="lg" />
              <PriceDisplay amountCents={1800} mode="from" size="md" />
              <VariantSelector
                label="Finish"
                selectedId={variant}
                onSelect={setVariant}
                options={[
                  { id: "v-sand", label: "Sand" },
                  { id: "v-oat", label: "Oat" },
                  { id: "v-char", label: "Char", disabled: true },
                ]}
              />
              <div className="flex items-center gap-2">
                <QuantityStepper value={qty} onChange={setQty} label="Stoneware mug" />
                <Button size="sm">Add to cart</Button>
              </div>
            </div>

            <div className="rounded-md border border-sf-border bg-sf-surface p-4">
              <p className="mb-1 text-label font-semibold text-sf-foreground">Your cart</p>
              <MediaPicker
                value={mediaValue}
                onChange={setMediaValue}
                media={MEDIA}
                className="mb-4"
              />
              {CART_LINES.map((line) => (
                <CartLineItem
                  key={line.id}
                  name={line.name}
                  image={line.image}
                  unitAmountCents={line.unitAmountCents}
                  quantity={lineQty[line.id]}
                  onQuantityChange={(next) => setLineQty({ ...lineQty, [line.id]: next })}
                  onRemove={() =>
                    setLineQty((current) => {
                      const copy = { ...current }
                      delete copy[line.id]
                      return copy
                    })
                  }
                />
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}