"use client";

import { usePathname } from "next/navigation";

import { SidebarTrigger } from "@/components/ui/sidebar";

/**
 * Home (/admin) owns the whole canvas — no bar at all. Every other admin page
 * gets a bare row whose only practical content is the mobile drawer trigger;
 * the nav chrome (logo, collapse, search, account) lives in the sidebar, so on
 * desktop this contributes zero height.
 */
export function AdminTopbar() {
  const pathname = usePathname();

  if (pathname === "/admin") return null;

  return (
    <div className="flex h-12 shrink-0 items-center px-3 md:hidden">
      <SidebarTrigger className="-ml-1" />
    </div>
  );
}