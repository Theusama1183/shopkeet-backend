import type { Metadata } from "next";

import { getSession } from "@/lib/session";
import { getTenantSettings } from "@/lib/store-actions";
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar";
import { AdminSidebar } from "@/components/admin/admin-sidebar";
import { AdminTopbar } from "@/components/admin/admin-topbar";
import { CommandPalette } from "@/components/admin/command-palette";

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
 * shell only reads the session to decorate the sidebar footer (store name, email)
 * and degrades to a bare sidebar when there isn't one yet.
 *
 * The command palette is mounted exactly once here: it owns the global Ctrl+K
 * shortcut and the record-search state, and the sidebar search bar opens it via
 * useOpenCommandPalette.
 */
export default async function AdminLayout({ children }: { children: React.ReactNode }) {
  const session = await getSession();
  const claims = session?.claims;

  // Only hydrate the account switcher when a store-scoped session exists; the
  // store-list and onboarding pages render with just a ticket, and the settings
  // read would redirect() on a phantom session.
  let storeName: string | undefined;
  if (session) {
    try {
      storeName = (await getTenantSettings()).name;
    } catch {
      // Shell must not break over an unreadable name — footer falls back to "Store".
    }
  }

  return (
    <div className="admin-surface [font-family:var(--font-inter)] min-h-full flex-1">
      <CommandPalette>
        <SidebarProvider>
          <AdminSidebar storeName={storeName} email={claims?.user_id} />
          <SidebarInset>
            <AdminTopbar />
            <div className="flex-1 space-y-5 p-5">{children}</div>
          </SidebarInset>
        </SidebarProvider>
      </CommandPalette>
    </div>
  );
}