import { Breadcrumbs, type Crumb } from "@/components/admin/breadcrumbs"

export interface PageHeaderProps {
  crumbs?: Crumb[]
  title: string
  description?: string
  /** Primary action slot — pushed to the far end of the header row. */
  actions?: React.ReactNode
}

/**
 * Breadcrumb + title + primary action, identical across every admin screen so
 * navigation reads as one coherent product rather than twenty.
 */
export function PageHeader({ crumbs, title, description, actions }: PageHeaderProps) {
  return (
    <header className="flex flex-col gap-3 px-6 pt-5 pb-4">
      {crumbs && crumbs.length > 0 ? <Breadcrumbs items={crumbs} /> : null}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-h1 font-semibold tracking-tight text-foreground">{title}</h1>
          {description ? (
            <p className="mt-1 max-w-xl text-body text-muted-foreground">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </div>
    </header>
  )
}