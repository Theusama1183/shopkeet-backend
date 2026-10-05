import { Building2Icon } from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import Link from "next/link";

/**
 * Honest placeholder for flows whose backend decision is still pending
 * (docs/04 Phase 1 has no OTP or password-reset endpoint yet — 13-spec flags
 * the Email-vs-SMS call explicitly). These pages must not dead-end with a form
 * that posts to nothing.
 */
export function PendingAuthFlow({
  title,
  description,
  href,
  hrefLabel = "Back to log in",
}: {
  title: string;
  description: string;
  href: string;
  hrefLabel?: string;
}) {
  return (
    <div className="grid gap-4">
      <Alert>
        <Building2Icon aria-hidden="true" />
        <AlertTitle>
          <Badge variant="outline" className="mr-1.5 align-middle">
            Coming soon
          </Badge>
          {title}
        </AlertTitle>
        <AlertDescription>{description}</AlertDescription>
      </Alert>
      <Button variant="outline" asChild>
        <Link href={href}>{hrefLabel}</Link>
      </Button>
    </div>
  );
}