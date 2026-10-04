import { cn } from "cn"

export function Section({
  id,
  title,
  description,
  children,
  className,
}: {
  id?: string
  title: string
  description?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <section id={id} className={cn("flex flex-col gap-4", className)}>
      <div>
        <h2 className="text-h2 font-semibold tracking-tight text-foreground">{title}</h2>
        {description ? (
          <p className="mt-1 max-w-2xl text-body text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {children}
    </section>
  )
}