import Link from "next/link"

import { ModeFrame } from "@/components/dev/mode-frame"
import { ThemeToggle } from "@/components/dev/theme-toggle"
import { PrimitivesDemo } from "@/components/dev/primitives-demo"
import { AdminDemo } from "@/components/dev/admin-demo"
import { StorefrontDemo } from "@/components/dev/storefront-demo"
import { Section } from "@/components/dev/section"
import { Button } from "@/components/ui/button"
import { StatusBadge } from "@/components/admin/status-badge"

export default function ComponentsPage() {
  return (
    <main className="mx-auto flex w-full max-w-6xl flex-col gap-10 px-4 py-6 sm:px-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="max-w-2xl">
          <div className="flex items-center gap-2">
            <StatusBadge tone="warning">Dev only</StatusBadge>
            <span className="text-caption tracking-wide text-muted-foreground uppercase">
              Phase 0–1 foundation — gates out of production builds
            </span>
          </div>
          <h1 className="mt-2 text-display font-semibold tracking-tight text-foreground">
            Shopkeet component library
          </h1>
          <p className="mt-2 text-lead text-muted-foreground">
            Every interactive surface below is built from the token system in{" "}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-caption">
              app/globals.css
            </code>
            . QA both light and dark on each tier; the storefront section proves per-tenant
            re-theming with no code change.
          </p>
        </div>
        <ThemeToggle />
      </header>

      <ModeFrame className="gap-6">
        <PrimitivesDemo />
      </ModeFrame>

      <ModeFrame>
        <AdminDemo />
      </ModeFrame>

      <Section
        id="storefront"
        title="Storefront & per-tenant theming"
        description="Tenant settings map onto the --sf-* variables at render time (lib/storefront-theme.ts). Toggle on a custom brand to watch every storefront utility re-theme live — the Phase 0 acceptance criterion."
      >
        <StorefrontDemo />
      </Section>

      <footer className="flex items-center justify-between border-t border-border pt-6 text-caption text-muted-foreground">
        <span>Foundation build — acceptance gate for Phase 0–1.</span>
        <Button asChild variant="ghost" size="sm">
          <Link href="/">Back to home preview</Link>
        </Button>
      </footer>
    </main>
  )
}