"use client"

import { useEffect } from "react"

/**
 * Root error boundary: replaces the root layout entirely, so it must ship its
 * own <html>/<body>. Global styles don't reach it (Next 16 docs) — styling is
 * inline and respects the OS color scheme via `color-scheme`.
 */
export default function GlobalError({
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
    <html lang="en" style={{ colorScheme: "light dark" }}>
      <body
        style={{
          margin: 0,
          minHeight: "100dvh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          padding: "1.5rem",
          fontFamily:
            'ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif',
          background: "var(--background, #f7f7f8)",
          color: "var(--foreground, #181a1f)",
        }}
      >
        <div style={{ maxWidth: "28rem", textAlign: "center" }}>
          <p
            style={{
              fontSize: "0.8125rem",
              letterSpacing: "0.06em",
              textTransform: "uppercase",
              color: "var(--muted-foreground, #5d6470)",
            }}
          >
            500
          </p>
          <h1 style={{ fontSize: "1.75rem", lineHeight: 1.2, margin: "0.5rem 0 0" }}>
            Something went wrong
          </h1>
          <p
            style={{
              marginTop: "0.5rem",
              fontSize: "0.9375rem",
              color: "var(--muted-foreground, #5d6470)",
            }}
          >
            This section couldn&apos;t be loaded. Try again.
          </p>
          <button
            onClick={() => retry()}
            style={{
              marginTop: "1.25rem",
              padding: "0.5rem 1rem",
              borderRadius: "0.4375rem",
              border: "1px solid var(--border, #e3e4e8)",
              background: "var(--card, #fff)",
              color: "inherit",
              font: "inherit",
              cursor: "pointer",
            }}
          >
            Try again
          </button>
        </div>
      </body>
    </html>
  )
}