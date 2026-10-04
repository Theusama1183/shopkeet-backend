export type StorefrontThemeMode = "light" | "dark"

export interface StorefrontThemeConfig {
  brand?: string
  brandForeground?: string
  brandSoft?: string
}

const HEX_RE = /^#?([0-9a-f]{6})$/i

type RGB = [number, number, number]

function parseHex(value: string): RGB | null {
  const match = HEX_RE.exec(value.trim())
  if (!match) return null
  const n = parseInt(match[1], 16)
  return [(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff]
}

function toHex([r, g, b]: RGB): string {
  const hex = (v: number) =>
    Math.min(255, Math.max(0, Math.round(v))).toString(16).padStart(2, "0")
  return `#${hex(r)}${hex(g)}${hex(b)}`
}

function luminance([r, g, b]: RGB): number {
  const channel = (c: number) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function mix(a: RGB, b: RGB, weightB: number): RGB {
  const t = Math.min(1, Math.max(0, weightB))
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t]
}

const WHITE: RGB = [255, 255, 255]

/** Picks black or white text for a background color, balanced toward a dark ink. */
export function contrastText(color: string): string {
  const rgb = parseHex(color)
  if (!rgb) return "#ffffff"
  return luminance(rgb) > 0.4 ? "#171a1e" : "#ffffff"
}

/**
 * Maps a tenant's storefront settings onto the `--sf-*` CSS variables that the
 * storefront token set is built on. Returns an empty object when no brand is
 * configured, so the fallback tokens in globals.css apply untouched.
 *
 * Dark mode lightens the displayed brand (a color tuned for a white canvas
 * rarely clears contrast on a dark one) and derives the soft tint from that.
 */
export function storefrontThemeVars(
  config: StorefrontThemeConfig,
  mode: StorefrontThemeMode = "light"
): Record<string, string> {
  const brand = parseHex(config.brand ?? "")
  if (!brand) return {}

  const display = mode === "dark" ? mix(brand, WHITE, 0.35) : brand
  const displayHex = toHex(display)
  const foreground = config.brandForeground ?? contrastText(displayHex)

  const brandSoft = config.brandSoft
    ? config.brandSoft
    : mode === "dark"
      ? toHex(mix(display, WHITE, 0.22))
      : toHex(mix(brand, WHITE, 0.88))

  return {
    "--sf-brand": displayHex,
    "--sf-brand-foreground": foreground,
    "--sf-brand-soft": brandSoft,
    "--sf-accent": displayHex,
    "--sf-accent-foreground": foreground,
  }
}

/** Parses a hex into { r, g, b } for pre-render validation of stored settings. */
export function isValidHexColor(value: string): boolean {
  return HEX_RE.test(value.trim())
}