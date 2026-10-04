import Link from "next/link"
import { ArrowRightIcon, PanelLeftIcon, StoreIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { StatusBadge } from "@/components/admin/status-badge"

/**
 * Transient foundation landing. The admin shell/sidebar lands with the first
 * real admin screen; this page is a dev-only index until then.
 */
export default function Home() {
  return (
    <main className="mx-auto flex min-h-svh w-full max-w-5xl flex-col justify-center px-6 py-12">
      <div className="flex flex-col gap-2">
        <StatusBadge tone="info">Foundation</StatusBadge>
        <h1 className="text-display font-semibold tracking-tight text-foreground">Shopkeet</h1>
        <p className="max-w-xl text-lead text-muted-foreground">
          The web app foundation (tokens, themed component library, API client, error wiring) is
          in place. Admin and storefront screens build on it phase by phase.
        </p>
      </div>

      <div className="mt-8 grid gap-4 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <PanelLeftIcon className="size-4 text-muted-foreground" aria-hidden="true" />
              Admin file
            </CardTitle>
            <CardDescription>Inside the app.</CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild>
              <Link href="/dev/components">
                Component library <ArrowRightIcon className="size-4" aria-hidden="true" />
              </Link>
            </Button>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <StoreIcon className="size-4 text-muted-foreground" aria-hidden="true" />
              Storefront file
            </CardTitle>
            <CardDescription>Screens begin after the foundation gate passes.</CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild variant="outline">
              <Link href="/dev/components#storefront">See the storefront re-theming demo</Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    </main>
  )
}