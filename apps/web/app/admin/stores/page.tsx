import type { Metadata } from "next";
import Link from "next/link";
import { StoreIcon } from "lucide-react";

import { getMerchantStores } from "@/lib/store-actions";
import { StoreList } from "@/components/admin/store-list";
import { PageHeader } from "@/components/admin/page-header";
import { EmptyState } from "@/components/feedback/empty-state";
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
    <div className="mx-auto w-full max-w-3xl space-y-5">
      <PageHeader
        title="Your stores"
        icon={StoreIcon}
        description={
          list.length === 1
            ? "The store you have access to."
            : `${list.length} stores you have access to. Pick one to open.`
        }
        actions={
          <Button asChild variant="ghost" size="sm">
            <Link href="/">Dashboard</Link>
          </Button>
        }
      />

      {list.length === 0 ? (
        <EmptyState
          title="No stores found"
          description="Your session may have expired — log in again to continue."
          action={
            <form action={clearSessionAction} className="inline">
              <Button type="submit" variant="outline" size="sm">
                Log in again
              </Button>
            </form>
          }
        />
      ) : (
        <StoreList stores={list} />
      )}
    </div>
  );
}
