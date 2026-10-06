import type { Metadata } from "next";

import { getSession } from "@/lib/session";
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar";
import { AdminSidebar } from "@/components/admin/admin-sidebar";
import { AdminTopbar } from "@/components/admin/admin-topbar";

export const metadata: Metadata = {
  title: {
    default: "Admin · Shopkeet",
    template: "%s · Shopkeet",
  },
};

/**
 * The unified admin surface (admin.<ROOT_DOMAIN>): chrome for every admin page.
 *
 * The auth gate is NOT here — it lives in (guarded)/layout.tsx so the store list
 * and the setup wizard can sit outside it. That matters twice: a merchant who
 * just logged into a multi-store account has no session until they pick a store,
 * and a signup session must be able to reach the wizard it is bounced to. This
 * shell only reads the session to decorate the topbar, and degrades to a bare
 * header when there isn't one yet.
 */
export default async function AdminLayout({ children }: { children: React.ReactNode }) {
  const session = await getSession();
  const claims = session?.claims;

  return (
    <div className="admin-surface [font-family:var(--font-inter)] min-h-full flex-1">
      <SidebarProvider>
        <AdminSidebar />
        <SidebarInset>
          <AdminTopbar
            user={claims?.user_id}
            store={claims?.tenant_id.slice(0, 8)}
            role={claims?.role}
          />
          <div className="flex-1 space-y-5 p-5">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </div>
  );
}