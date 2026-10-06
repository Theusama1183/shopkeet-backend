"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { ChevronsRightIcon, ChevronsUpDownIcon, LogOutIcon, SearchIcon, StoreIcon } from "lucide-react";

import { Logo } from "@/components/ui/logo";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
  useSidebar,
} from "@/components/ui/sidebar";
import { clearSessionAction } from "@/lib/admin-actions";
import { ADMIN_NAV_ITEMS, type AdminNavItem } from "@/lib/admin-nav";
import { useOpenCommandPalette } from "@/components/admin/command-palette";

/**
 * The Polaris-register sidebar: logo + collapse living in the header, one
 * instant-search bar that opens the command palette, a pinned account switcher
 * in the footer (this replaces the topbar dropdown of the first pass). When the
 * sidebar is collapsed the logo slot swaps to an affordance on hover: rest on
 * the icon, hover to reveal an expand chevron in the same slot.
 */
export function AdminSidebar({
  storeName,
  email,
}: {
  storeName?: string;
  email?: string;
}) {
  const pathname = usePathname();
  const { state, toggleSidebar } = useSidebar();
  const openPalette = useOpenCommandPalette();

  const isActive = (item: AdminNavItem) =>
    item.match === "exact" ? pathname === item.href : pathname === item.href || pathname.startsWith(`${item.href}/`);

  // Collapsed logo = expand gesture; expanded logo = home.
  const onLogoClick = (event: React.MouseEvent) => {
    if (state === "collapsed") {
      event.preventDefault();
      toggleSidebar();
    }
  };

  const initials = (storeName ?? email ?? "Store")
    .split(/[\s._-]+/)
    .filter(Boolean)
    .map((part) => part[0])
    .join("")
    .slice(0, 2)
    .toUpperCase() || "?";

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="pt-3">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              size="lg"
              className="group-data-[collapsible=icon]:justify-center"
            >
              <Link href="/admin" aria-label="Shopkeet admin home" onClick={onLogoClick}>
                <span className="relative flex size-12 shrink-0 items-center justify-center">
                  <Logo
                    variant="icon"
                    size="lg"
                    className="text-sidebar-primary transition-opacity duration-200 group-hover:group-data-[collapsible=icon]:opacity-0"
                  />
                  <ChevronsRightIcon
                    className="absolute size-5 text-sidebar-foreground opacity-0 transition-opacity duration-200 group-hover:group-data-[collapsible=icon]:opacity-100 group-data-[state=expanded]:hidden"
                    aria-hidden="true"
                  />
                </span>
                <Logo
                  variant="text"
                  size="md"
                  className="text-sidebar-foreground group-data-[state=collapsed]:hidden"
                />
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarMenuItem>
            <button
              type="button"
              onClick={openPalette}
              className="flex h-9 w-full items-center gap-2 rounded-lg border border-sidebar-border bg-sidebar-accent/60 px-3 text-body text-sidebar-foreground/80 outline-hidden transition-colors hover:bg-sidebar-accent hover:text-sidebar-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring group-data-[collapsible=icon]:hidden"
            >
              <SearchIcon className="size-4 shrink-0" aria-hidden="true" />
              <span className="flex-1 truncate text-left">Search</span>
              <kbd className="rounded border border-sidebar-border bg-sidebar-accent px-1.5 py-0.5 font-sans text-caption leading-none text-sidebar-foreground/50">
                ⌘K
              </kbd>
            </button>
          </SidebarMenuItem>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Navigate</SidebarGroupLabel>
          <SidebarMenu>
            {ADMIN_NAV_ITEMS.map((item) => (
              <SidebarMenuItem key={item.href}>
                <SidebarMenuButton
                  asChild
                  isActive={isActive(item)}
                  tooltip={item.title}
                  className="data-[active=true]:[&_svg]:text-sidebar-primary"
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

      <SidebarFooter>
        <SidebarSeparator className="mx-1.5 w-auto" />
        <SidebarMenu>
          <SidebarMenuItem>
            {storeName || email ? (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <SidebarMenuButton size="lg" tooltip={storeName ?? email} className="group-data-[collapsible=icon]:justify-center">
                    <Avatar className="size-8 shrink-0">
                      <AvatarFallback className="bg-sidebar-primary text-label font-medium text-sidebar-primary-foreground">
                        {initials}
                      </AvatarFallback>
                    </Avatar>
                    <div className="flex min-w-0 flex-1 flex-col items-start gap-0.5 group-data-[collapsible=icon]:hidden">
                      <span className="truncate text-body font-medium text-sidebar-foreground">
                        {storeName ?? "Store"}
                      </span>
                      {email ? (
                        <span className="truncate text-caption text-sidebar-foreground/60">{email}</span>
                      ) : null}
                    </div>
                    <ChevronsUpDownIcon
                      className="size-4 shrink-0 text-sidebar-foreground/50 group-data-[collapsible=icon]:hidden"
                      aria-hidden="true"
                    />
                  </SidebarMenuButton>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" side="top" className="w-64">
                  <DropdownMenuLabel>
                    <p className="text-label font-medium text-foreground">{storeName ?? "Store"}</p>
                    {email ? <p className="text-caption text-muted-foreground">{email}</p> : null}
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem asChild>
                    <Link href="/admin/stores">
                      <StoreIcon aria-hidden="true" />
                      All stores
                    </Link>
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    onSelect={() => {
                      void clearSessionAction();
                    }}
                  >
                    <LogOutIcon aria-hidden="true" />
                    Log out
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            ) : (
              <SidebarMenuButton asChild size="lg" tooltip="All stores" className="group-data-[collapsible=icon]:justify-center">
                <Link href="/admin/stores">
                  <StoreIcon className="size-5 shrink-0" aria-hidden="true" />
                  <span className="group-data-[collapsible=icon]:hidden">All stores</span>
                </Link>
              </SidebarMenuButton>
            )}
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}