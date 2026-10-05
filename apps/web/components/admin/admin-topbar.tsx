"use client";

import { LogOutIcon } from "lucide-react";

import { clearSessionAction } from "@/lib/admin-actions";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";

/**
 * Merchant identity comes from the session, so all three are optional: a browser
 * holding only a store_pick ticket (just logged in, no store chosen yet) has no
 * session to read from, and gets the bare header while it picks.
 */
export function AdminTopbar({
  user,
  store,
  role,
}: {
  user?: string;
  store?: string;
  role?: string;
}) {
  const initials = (user ?? "")
    .split("@")[0]
    .split(/[._-]/)
    .map((part) => part[0])
    .join("")
    .slice(0, 2)
    .toUpperCase() || "?";

  return (
    <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b border-border bg-background/80 px-3 backdrop-blur">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 h-5" />
      <div className="flex flex-1 items-center gap-1" />

      {user && store && role ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="gap-2 px-2">
              <Avatar className="size-6">
                <AvatarFallback className="text-caption">{initials}</AvatarFallback>
              </Avatar>
              <span className="text-label font-medium">{store}</span>
              <span className="hidden text-caption text-muted-foreground sm:inline">· {role}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel>
              <p className="text-label text-foreground">{store}</p>
              <p className="text-caption text-muted-foreground">{user}</p>
            </DropdownMenuLabel>
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
      ) : null}
    </header>
  );
}