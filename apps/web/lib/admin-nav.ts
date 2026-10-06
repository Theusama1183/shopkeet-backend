import type { LucideIcon } from "lucide-react"
import {
  BarChart3Icon,
  FileTextIcon,
  HomeIcon,
  PackageIcon,
  SettingsIcon,
  ShoppingBagIcon,
  StoreIcon,
  TagIcon,
  TrendingUpIcon,
  UsersIcon,
} from "lucide-react"

export interface AdminNavItem {
  title: string
  href: string
  icon: LucideIcon
  /** Exact-match for Home (/admin); prefix for everything else. */
  match: "exact" | "prefix"
}

/**
 * The admin sections, in the order the 12-spec's Shopify-based nav defines
 * (POS items are deliberately absent). Shared by the sidebar (client) and the
 * per-page PageHeader icons (server), so one list owns every nav label and
 * no page can drift from what the sidebar shows.
 */
export const ADMIN_NAV_ITEMS: AdminNavItem[] = [
  { title: "Home", href: "/admin", icon: HomeIcon, match: "exact" },
  // Stores comes before the store-scoped sections: for a merchant running
  // several stores it is the switcher, and it is the page login lands on.
  { title: "Stores", href: "/admin/stores", icon: StoreIcon, match: "prefix" },
  { title: "Orders", href: "/admin/orders", icon: ShoppingBagIcon, match: "prefix" },
  { title: "Products", href: "/admin/products", icon: PackageIcon, match: "prefix" },
  { title: "Customers", href: "/admin/customers", icon: UsersIcon, match: "prefix" },
  { title: "Growth", href: "/admin/growth", icon: TrendingUpIcon, match: "prefix" },
  { title: "Discounts", href: "/admin/discounts", icon: TagIcon, match: "prefix" },
  { title: "Content", href: "/admin/content", icon: FileTextIcon, match: "prefix" },
  { title: "Analytics", href: "/admin/analytics", icon: BarChart3Icon, match: "prefix" },
  { title: "Settings", href: "/admin/settings", icon: SettingsIcon, match: "prefix" },
]

/** The nav item whose href owns the given path (used for page-title icons). */
export function navItemForPath(pathname: string): AdminNavItem | undefined {
  return ADMIN_NAV_ITEMS.find((item) =>
    item.match === "exact" ? pathname === item.href : pathname === item.href || pathname.startsWith(`${item.href}/`)
  )
}