"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandLoading,
  CommandSeparator,
} from "cmdk";
import {
  PackageIcon,
  SearchIcon,
  ShoppingBagIcon,
  UsersIcon,
} from "lucide-react";

import { ADMIN_NAV_ITEMS } from "@/lib/admin-nav";
import type { SearchResponse } from "@/lib/admin-types";
import { formatMoney } from "@/lib/format";
import { cn } from "cn";

/**
 * Lets the sidebar search bar (and anything else on the admin surface) open
 * the palette without each caller owning its own keydown handler. Mounted once
 * (app/admin/layout.tsx) so Ctrl+K never registers twice; layout children render
 * inside the provider so consumers can call useOpenCommandPalette.
 */
const PaletteContext = React.createContext<{ open: () => void } | null>(null);

export function useOpenCommandPalette() {
  const ctx = React.useContext(PaletteContext);
  if (!ctx) throw new Error("useOpenCommandPalette must be used within <CommandPalette>");
  return ctx.open;
}

const SEARCH_DEBOUNCE_MS = 250;

export function CommandPalette({ children }: { children?: React.ReactNode }) {
  const router = useRouter();
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [records, setRecords] = React.useState<SearchResponse | null>(null);
  const [status, setStatus] = React.useState<"idle" | "loading" | "done" | "error">("idle");

  // Open with fresh state (resets happen here, an event handler, never in an
  // effect body — new keystrokes get an empty menu instead of last time's).
  const openWithFreshState = React.useCallback(() => {
    setQuery("");
    setRecords(null);
    setStatus("idle");
    setOpen(true);
  }, []);

  React.useEffect(() => {
    const down = (event: KeyboardEvent) => {
      if (event.key === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        if (open) setOpen(false);
        else openWithFreshState();
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [open, openWithFreshState]);

  // Debounced record search. No fetch (and no state churn) on an empty query;
  // render-side guards keep stale groups off the list in that case.
  React.useEffect(() => {
    const q = query.trim();
    if (!q) return;
    const task = setTimeout(async () => {
      try {
        const res = await fetch(`/api/admin/search?q=${encodeURIComponent(q)}`);
        if (res.ok) {
          setRecords((await res.json()) as SearchResponse);
          setStatus("done");
        } else {
          setRecords(null);
          setStatus("error");
        }
      } catch {
        setRecords(null);
        setStatus("error");
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(task);
  }, [query]);

  // Keystrokes drive the loading/empty states directly (not from an effect):
  // any change drops the old results and marks the fetch as in-flight until the
  // debounced request lands.
  const handleQueryChange = (value: string) => {
    setQuery(value);
    setRecords(null);
    setStatus(value.trim() ? "loading" : "idle");
  };

  const go = (href: string) => {
    setOpen(false);
    router.push(href);
  };

  const searching = query.trim().length > 0;

  return (
    <PaletteContext.Provider value={React.useMemo(() => ({ open: openWithFreshState }), [openWithFreshState])}>
      {children}

      <CommandDialog
        open={open}
        onOpenChange={setOpen}
        label="Store search"
        contentClassName={cn(
          "overflow-hidden rounded-xl border border-border bg-popover text-popover-foreground shadow-xl",
          "data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95"
        )}
        overlayClassName="data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0 bg-black/40"
      >
        <CommandInput
          autoFocus
          value={query}
          onValueChange={handleQueryChange}
          placeholder="Search your store…"
          className="h-12 border-b border-border bg-transparent px-4 text-body outline-hidden placeholder:text-muted-foreground disabled:pointer-events-none disabled:opacity-50"
        />
        <CommandList aria-hidden={false}>
          <CommandEmpty className="py-6 text-center text-body text-muted-foreground">
            No results for {query ? `“${query}”` : "that"}.
          </CommandEmpty>

          <CommandGroup heading="Go to">
            {ADMIN_NAV_ITEMS.map((item) => (
              <CommandItem
                key={item.href}
                value={item.title}
                onSelect={() => go(item.href)}
                className="flex items-center gap-3 px-4 py-2.5 text-body"
              >
                <item.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="flex-1 truncate">{item.title}</span>
                {item.href !== "/admin" ? (
                  <span className="text-caption text-muted-foreground/70">{item.href.replace("/admin/", "")}</span>
                ) : null}
              </CommandItem>
            ))}
          </CommandGroup>

          {status === "loading" ? (
            <CommandLoading className="px-4 py-3 text-caption text-muted-foreground">Searching…</CommandLoading>
          ) : null}

          {status === "error" ? (
            <CommandGroup heading="Search" forceMount>
              <div className="px-4 py-3 text-caption text-destructive">Search is unavailable right now.</div>
            </CommandGroup>
          ) : null}

          {searching && records ? (
            <>
              <CommandSeparator />
              {records.products.length > 0 ? (
                <CommandGroup heading="Products">
                  {records.products.map((product) => (
                    <CommandItem
                      key={product.id}
                      value={`product-${product.name}-${query}`}
                      forceMount
                      onSelect={() => go("/admin/products")}
                      className="flex items-center gap-3 px-4 py-2.5 text-body"
                    >
                      <PackageIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span className="flex-1 truncate">{product.name}</span>
                      <StatusPill status={product.status} />
                    </CommandItem>
                  ))}
                </CommandGroup>
              ) : null}
              {records.orders.length > 0 ? (
                <CommandGroup heading="Orders">
                  {records.orders.map((order) => (
                    <CommandItem
                      key={order.id}
                      value={`order-${order.customer_name}-${query}`}
                      forceMount
                      onSelect={() => go(`/admin/orders/${order.id}`)}
                      className="flex items-center gap-3 px-4 py-2.5 text-body"
                    >
                      <ShoppingBagIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span className="flex-1 truncate">{order.customer_name}</span>
                      <StatusPill status={order.status} className="hidden sm:inline-flex" />
                      <span className="text-label font-medium tabular-nums text-foreground">
                        {formatMoney(order.total_cents, order.currency)}
                      </span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              ) : null}
              {records.customers.length > 0 ? (
                <CommandGroup heading="Customers">
                  {records.customers.map((customer) => (
                    <CommandItem
                      key={customer.id}
                      value={`customer-${customer.email || customer.phone}-${query}`}
                      forceMount
                      onSelect={() => go("/admin/customers")}
                      className="flex items-center gap-3 px-4 py-2.5 text-body"
                    >
                      <UsersIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span className="flex-1 truncate">{customer.email || customer.phone || "Anonymous"}</span>
                      {customer.email && customer.phone ? (
                        <span className="text-caption text-muted-foreground/70">{customer.phone}</span>
                      ) : null}
                    </CommandItem>
                  ))}
                </CommandGroup>
              ) : null}
            </>
          ) : null}
        </CommandList>

        <div className="flex items-center gap-4 border-t border-border px-4 py-2.5 text-caption text-muted-foreground">
          <span className="flex items-center gap-1">
            <kbd className="font-sans text-[11px] text-foreground/70">↑↓</kbd> navigate
          </span>
          <span className="flex items-center gap-1">
            <kbd className="font-sans text-[11px] text-foreground/70">↵</kbd> open
          </span>
          <span className="ml-auto hidden items-center gap-1 sm:flex">
            <kbd className="flex size-4 items-center justify-center rounded border border-border font-sans text-[11px] text-foreground/70">
              <SearchIcon className="size-3" aria-hidden="true" />
            </kbd>
            search your store
          </span>
        </div>
      </CommandDialog>
    </PaletteContext.Provider>
  );
}

/** Tiny status pill for palette rows (products/orders). */
function StatusPill({ status, className }: { status: string; className?: string }) {
  const label = status.replace("_", " ");
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-md bg-muted px-1.5 py-0.5 text-caption font-medium text-muted-foreground",
        className
      )}
    >
      {label}
    </span>
  );
}