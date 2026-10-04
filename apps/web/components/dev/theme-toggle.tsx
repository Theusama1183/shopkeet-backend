"use client"

import { useTheme } from "next-themes"
import { MonitorIcon, MoonIcon, SunIcon } from "lucide-react"

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export function ThemeToggle() {
  const { setTheme, theme } = useTheme()
  return (
    <Select value={theme} onValueChange={(value) => setTheme(value)} aria-label="Color scheme">
      <SelectTrigger size="sm" className="w-fit min-w-24">
        <SelectValue placeholder="Theme" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="light">
          <span className="inline-flex items-center gap-2">
            <SunIcon className="size-3.5" /> Light
          </span>
        </SelectItem>
        <SelectItem value="dark">
          <span className="inline-flex items-center gap-2">
            <MoonIcon className="size-3.5" /> Dark
          </span>
        </SelectItem>
        <SelectItem value="system">
          <span className="inline-flex items-center gap-2">
            <MonitorIcon className="size-3.5" /> System
          </span>
        </SelectItem>
      </SelectContent>
    </Select>
  )
}