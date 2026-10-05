import { PageHeader } from "@/components/admin/page-header";
import { Card, CardContent } from "@/components/ui/card";

/**
 * Honest placeholder for admin sections that aren't built yet. The shell,
 * session guard, and layout conventions stay in place; only the surface is a
 * stub. Replace the page that renders this with a real one as phases land.
 */
export function ComingSoon({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <>
      <PageHeader title={title} description={description} />
      <Card>
        <CardContent className="flex min-h-40 flex-col items-center justify-center gap-1 text-center">
          <p className="text-sm font-medium text-foreground">This section is on its way</p>
          <p className="max-w-md text-caption text-muted-foreground">
            The API and this shell are ready — the management surface lands in an upcoming build phase.
          </p>
        </CardContent>
      </Card>
    </>
  );
}