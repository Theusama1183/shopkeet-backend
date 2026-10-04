"use client"

import { useState } from "react"
import { MoonIcon, SunIcon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"

/**
 * Previews its children in the light or dark token set locally — both admin
 * and storefront tokens flip under `.dark`, so a frame is enough to QA a
 * component in both modes without touching the app-wide theme.
 */
export function ModeFrame({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  const [mode, setMode] = useState<"light" | "dark">("light")

  return (
    <div className={cn("flex flex-col gap-2", className)}>
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
      <div className={cn(mode === "dark" && "dark", "rounded-lg border border-border bg-background")}>
        {children}
      </div>
    </div>
  )
}