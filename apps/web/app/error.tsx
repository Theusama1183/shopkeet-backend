"use client"

import { useEffect } from "react"

import { ErrorState } from "@/components/feedback/error-state"

/**
 * Per-route error boundary. Next 16: `retry()` re-fetches and re-renders the
 * segment — prefer it over reset().
 */
export default function Error({
  error,
  retry,
}: {
  error: Error & { digest?: string }
  retry: () => void
}) {
  useEffect(() => {
    console.error(error)
  }, [error])

  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <div className="w-full max-w-lg">
        <ErrorState
          title="Something went wrong"
          description="This section couldn't be loaded. Try again — if it keeps failing, the problem isn't on your side."
          onRetry={retry}
        />
      </div>
    </div>
  )
}