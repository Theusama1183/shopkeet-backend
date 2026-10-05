import type { Metadata } from "next";
import Link from "next/link";

import { getMerchantStores } from "@/lib/store-actions";
import { StoreList } from "@/components/admin/store-list";
import { Button } from "@/components/ui/button";
import { clearSessionAction } from "@/lib/admin-actions";

export const metadata: Metadata = {
  title: "Your stores · Shopkeet",
};

/**
 * Every store this account can open. Reached straight after login when the
 * merchant owns more than one, and always available from the sidebar so they can
 * switch without logging out. Reads through the store_pick cookie the login
 * handler left behind — or the current session when switching from the sidebar.
 */
export default async function StoresPage() {
  const list = await getMerchantStores();

  return (
    <div className="mx-auto w-full max-w-3xl space-y-8">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="space-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">Your stores</h1>
          <p className="text-muted-foreground">
            {list.length === 1
              ? "The store you have access to."
              : `${list.length} stores you have access to. Pick one to open.`}
          </p>
        </div>
        <Button asChild variant="ghost" size="sm">
          <Link href="/">Dashboard</Link>
        </Button>
      </header>

      {list.length === 0 ? (
        <div className="rounded-lg border border-dashed p-10 text-center">
          <p className="font-medium">No stores found</p>
          <p className="mt-1 text-caption text-muted-foreground">
            Your session may have expired.{" "}
            <form action={clearSessionAction} className="inline">
              <button type="submit" className="underline underline-offset-3">
                Log in again
              </button>
            </form>
            .
          </p>
        </div>
      ) : (
        <StoreList stores={list} />
      )}
    </div>
  );
}