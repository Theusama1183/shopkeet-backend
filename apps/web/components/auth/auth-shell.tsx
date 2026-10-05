import * as React from "react";

import { cn } from "@/lib/utils";
import { Logo } from "@/components/ui/logo";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export interface AuthShellProps {
  title: string;
  description: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
}

/**
 * The shared auth surface (auth.<ROOT_DOMAIN>). One centered card on the muted
 * admin background, the full Logo up top, cross-links in the footer.
 */
export function AuthShell({ title, description, children, footer }: AuthShellProps) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-muted/30 px-4 py-12">
      <div className="w-full max-w-md space-y-6">
        <div className="flex justify-center">
          <Logo size="lg" className="h-10 text-foreground" />
        </div>
        <Card className="shadow-sm">
          <CardHeader>
            <CardTitle className={cn("text-h2")}>{title}</CardTitle>
            <CardDescription className="text-body text-muted-foreground">{description}</CardDescription>
          </CardHeader>
          <CardContent>{children}</CardContent>
        </Card>
        {footer ? <div className="text-center text-caption text-muted-foreground">{footer}</div> : null}
      </div>
    </div>
  );
}