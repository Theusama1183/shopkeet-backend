import Link from "next/link"

import { Button } from "@/components/ui/button"

export default function NotFound() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-1 px-6 text-center">
      <p className="text-caption font-medium tracking-wide text-muted-foreground uppercase">
        404
      </p>
      <h1 className="mt-1 text-h1 font-semibold tracking-tight text-foreground">
        Page not found
      </h1>
      <p className="mt-1 max-w-sm text-body text-muted-foreground">
        The page you&apos;re after doesn&apos;t exist or has moved.
      </p>
      <Button asChild className="mt-5">
        <Link href="/">Back to Shopkeet</Link>
      </Button>
    </div>
  )
}