import type { LucideIcon } from "lucide-react"

import { Breadcrumbs, type Crumb } from "@/components/admin/breadcrumbs"

export interface PageHeaderProps {
  crumbs?: Crumb[]
  title: string
  /** Nav icon for the section, rendered immediately left of the title. */
  icon?: LucideIcon
  description?: string
  /** Primary action slot — pushed to the far end of the header row. */
  actions?: React.ReactNode
}

/**
 * Breadcrumb + title + primary action, identical across every admin screen so
 * navigation reads as one coherent product rather than twenty. Titles are the
 * Polaris register's 16px/500 so a page reads as a section, not a headline.
 */
export function PageHeader({ crumbs, title, icon: Icon, description, actions }: PageHeaderProps) {
  return (
    <header className="flex flex-col gap-2">
      {crumbs && crumbs.length > 0 ? <Breadcrumbs items={crumbs} className="text-label" /> : null}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="flex items-center gap-2 text-base font-medium tracking-tight text-foreground">
            {Icon ? <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" /> : null}
            {title}
          </h1>
          {description ? (
            <p className="mt-0.5 max-w-xl text-body text-muted-foreground">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </div>
    </header>
  )
}