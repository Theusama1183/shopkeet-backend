"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  HomeIcon,
  ShoppingBagIcon,
  PackageIcon,
  UsersIcon,
  TrendingUpIcon,
  TagIcon,
  FileTextIcon,
  BarChart3Icon,
  SettingsIcon,
  StoreIcon,
  type LucideIcon,
} from "lucide-react";

import { Logo } from "@/components/ui/logo";
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar";

interface AdminNavItem {
  title: string;
  href: string;
  icon: LucideIcon;
  /** Exact-match for Home (/admin); prefix for everything else. */
  match: "exact" | "prefix";
}

/**
 * The admin sections, in the order the 12-spec's Shopify-based nav defines
 * (POS items are deliberately absent). Sections still on the roadmap render a
 * placeholder page so nothing 404s.
 */
const NAV_ITEMS: AdminNavItem[] = [
  { title: "Home", href: "/admin", icon: HomeIcon, match: "exact" },
  // Stores comes before the store-scoped sections: for a merchant running several
  // stores it is the switcher, and it is the page login lands on.
  { title: "Stores", href: "/admin/stores", icon: StoreIcon, match: "prefix" },
  { title: "Orders", href: "/admin/orders", icon: ShoppingBagIcon, match: "prefix" },
  { title: "Products", href: "/admin/products", icon: PackageIcon, match: "prefix" },
  { title: "Customers", href: "/admin/customers", icon: UsersIcon, match: "prefix" },
  { title: "Growth", href: "/admin/growth", icon: TrendingUpIcon, match: "prefix" },
  { title: "Discounts", href: "/admin/discounts", icon: TagIcon, match: "prefix" },
  { title: "Content", href: "/admin/content", icon: FileTextIcon, match: "prefix" },
  { title: "Analytics", href: "/admin/analytics", icon: BarChart3Icon, match: "prefix" },
  { title: "Settings", href: "/admin/settings", icon: SettingsIcon, match: "prefix" },
];

export function AdminSidebar() {
  const pathname = usePathname();

  const isActive = (item: AdminNavItem) =>
    item.match === "exact" ? pathname === item.href : pathname === item.href || pathname.startsWith(`${item.href}/`);

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild size="lg" className="group-data-[state=collapsed]:justify-center">
              <Link href="/admin" aria-label="Shopkeet admin home">
                <span className="flex items-center justify-center group-data-[state=collapsed]:opacity-100">
                  <Logo variant="icon" size="md" className="text-sidebar-primary" />
                </span>
                <Logo variant="text" size="md" className="text-sidebar-foreground group-data-[state=collapsed]:hidden" />
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Navigate</SidebarGroupLabel>
          <SidebarMenu>
            {NAV_ITEMS.map((item) => (
              <SidebarMenuItem key={item.href}>
                <SidebarMenuButton
                  asChild
                  isActive={isActive(item)}
                  tooltip={item.title}
                >
                  <Link href={item.href}>
                    <item.icon />
                    <span>{item.title}</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  );
}