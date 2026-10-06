"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { ArrowRightIcon, Loader2Icon } from "lucide-react";

import type { MerchantStore } from "@/lib/admin-types";
import { apiErrorToast } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/admin/status-badge";
import { cn } from "@/lib/utils";

/**
 * The store picker. A merchant can run several stores, so login stops here when
 * there is more than one — no store is ever guessed. Picking one posts the
 * store_pick cookie to the API and the response sets the session, then the
 * browser reloads the dashboard.
 */
export function StoreList({ stores }: { stores: MerchantStore[] }) {
  const router = useRouter();
  const [pendingId, setPendingId] = React.useState<string | null>(null);
  const [error, setError] = React.useState<string | null>(null);

  async function openStore(store: MerchantStore) {
    setPendingId(store.tenant_id);
    setError(null);
    try {
      const res = await fetch("/api/auth/select-store", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ tenant_id: store.tenant_id }),
      });
      const payload = (await res.json().catch(() => null)) as
        | { error?: { message?: string }; onboarding_completed?: boolean }
        | null;
      if (!res.ok) {
        setError(payload?.error?.message ?? "Could not open that store.");
        setPendingId(null);
        return;
      }
      // A store still mid-onboarding belongs in the wizard, not the dashboard.
      const destination =
        payload?.onboarding_completed === false
          ? "/admin/onboarding"
          : "/admin";
      router.replace(destination);
      router.refresh();
    } catch {
      apiErrorToast(new Error("Could not reach the server. Check your connection."));
      setPendingId(null);
    }
  }

  return (
    <div className="grid gap-4">
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : null}

      {stores.map((store) => {
        const pending = pendingId === store.tenant_id;
        const suspended = store.status !== "active";

        return (
          <Card
            key={store.tenant_id}
            className={cn(pending && "ring-2 ring-primary")}
          >
            <CardContent className="flex items-center gap-4">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="truncate text-body font-medium">{store.name}</p>
                  {store.role !== "owner" ? (
                    <StatusBadge tone="neutral" className="capitalize">
                      {store.role}
                    </StatusBadge>
                  ) : null}
                  {suspended ? <StatusBadge tone="danger">Suspended</StatusBadge> : null}
                  {store.onboarding_completed ? null : (
                    <StatusBadge tone="warning">Setup incomplete</StatusBadge>
                  )}
                </div>
                <p className="truncate text-caption text-muted-foreground">
                  {store.subdomain}
                </p>
              </div>

              <Button
                onClick={() => openStore(store)}
                disabled={pendingId !== null || suspended}
                size="sm"
              >
                {pending ? (
                  <Loader2Icon className="animate-spin" aria-hidden="true" />
                ) : (
                  <ArrowRightIcon aria-hidden="true" />
                )}
                {pending ? "Opening" : "Open"}
              </Button>
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}