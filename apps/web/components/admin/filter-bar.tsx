import { SearchIcon } from "lucide-react"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"

export interface FilterField {
  id: string
  label?: string
  control: React.ReactNode
}

export interface FilterBarProps {
  search?: string
  onSearchChange?: (value: string) => void
  searchPlaceholder?: string
  fields?: FilterField[]
  actions?: React.ReactNode
}

/**
 * The standard filter/search row above a DataTable. One consistent layout so
 * every admin list screen scans the same way.
 */
export function FilterBar({
  search,
  onSearchChange,
  searchPlaceholder = "Search…",
  fields = [],
  actions,
}: FilterBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-3 px-6 py-3">
      {onSearchChange ? (
        <div className="relative min-w-52">
          <Input
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder={searchPlaceholder}
            aria-label={searchPlaceholder}
            className="pl-8"
          />
          <SearchIcon
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
        </div>
      ) : null}
      {fields.map((field, index) => (
        <div key={field.id} className="flex items-center gap-2">
          {index === 0 && search ? <Separator orientation="vertical" className="h-6" /> : null}
          {field.label ? <Label className="text-label text-muted-foreground">{field.label}</Label> : null}
          {field.control}
        </div>
      ))}
      {actions ? (
        <div className="ml-auto flex items-center gap-2">{actions}</div>
      ) : null}
    </div>
  )
}