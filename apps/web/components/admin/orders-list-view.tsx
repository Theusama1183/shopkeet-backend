"use client";

import * as React from "react";

import type { AdminOrder } from "@/lib/admin-types";
import { OrdersFilters } from "@/components/admin/orders-filters";
import { OrdersTable } from "@/components/admin/orders-table";

/**
 * Combines the filter bar (URL-driven status/payment) with a client-side text
 * search over the already-loaded page and the orders table. The page passes the
 * server-fetched orders; everything interactive stays in the browser.
 */
export function OrdersListView({ orders }: { orders: AdminOrder[] }) {
  const [search, setSearch] = React.useState("");

  return (
    <div className="space-y-4">
      <OrdersFilters search={search} onSearchChange={setSearch} />
      <OrdersTable orders={orders} query={search} />
    </div>
  );
}