import {
  storefrontThemeVars,
  type StorefrontThemeConfig,
  type StorefrontThemeMode,
} from "@/lib/storefront-theme"

/**
 * Server component that applies a tenant's theme to the storefront.
 *
 * Renders a <style> scoped to the element marked `data-storefront-root`, so the
 * tenant's palette never bleeds onto the admin surface and page-level caching
 * stays safe. When no brand is configured the fallback tokens stand.
 */
export function StorefrontTheme({
  theme,
  mode = "light",
}: {
  theme: StorefrontThemeConfig | null | undefined
  mode?: StorefrontThemeMode
}) {
  const vars = theme ? storefrontThemeVars(theme, mode) : {}
  const entries = Object.entries(vars)
  if (entries.length === 0) return null

  const declarations = entries.map(([key, value]) => `${key}:${value}`).join(";")
  return (
    <style data-storefront-theme="" media="" precedence="default">
      {`[data-storefront-root]{${declarations}}`}
    </style>
  )
}