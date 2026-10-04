import { notFound } from "next/navigation"

import { DevProviders } from "@/components/dev/providers"

const isDevGalleryEnabled =
  process.env.NODE_ENV === "development" || process.env.NEXT_PUBLIC_ENABLE_DEV_GALLERY === "true"

/**
 * /dev/components is a visual QA surface, never shipped to production. Gate it
 * behind a dev-only flag — a production build renders notFound() for this route.
 */
export default function DevLayout({ children }: LayoutProps<"/dev">) {
  if (!isDevGalleryEnabled) {
    notFound()
  }
  return (
    <div className="min-h-svh bg-background text-foreground">
      <DevProviders>{children}</DevProviders>
    </div>
  )
}