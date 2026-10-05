"use client";

import type { LegacyColumnDef } from "@tanstack/react-table/legacy";

import { formatMoney, formatDateTime } from "@/lib/format";
import type { AbandonedCart } from "@/lib/admin-types";
import { DataTable } from "@/components/admin/data-table";

export function AbandonedTable({ carts }: { carts: AbandonedCart[] }) {
  const columns: LegacyColumnDef<AbandonedCart, unknown>[] = [
    {
      id: "email",
      header: "Customer",
      cell: ({ row }) => (
        <div className="min-w-0">
          <p className="truncate font-medium text-foreground">{row.original.customer_email}</p>
        </div>
      ),
    },
    {
      id: "items",
      header: "Items",
      cell: ({ row }) => <span className="tabular-nums text-muted-foreground">{row.original.item_count}</span>,
    },
    {
      id: "total",
      header: "Total",
      cell: ({ row }) => (
        <span className="font-medium text-foreground tabular-nums">{formatMoney(row.original.total_cents)}</span>
      ),
    },
    {
      id: "activity",
      header: "Last activity",
      cell: ({ row }) => <span className="text-muted-foreground">{formatDateTime(row.original.last_activity_at)}</span>,
    },
    {
      id: "recovery",
      header: "Recovery email",
      cell: ({ row }) => (
        <span className={row.original.recovery_sent_at ? "text-success" : "text-muted-foreground"}>
          {row.original.recovery_sent_at ? "Sent" : "Not sent"}
        </span>
      ),
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={carts}
      getRowId={(cart) => cart.id}
      emptyTitle="No abandoned checkouts"
      emptyDescription="A cart counts as abandoned when a shopper enters their email, adds items, and never checks out."
      aria-label="Abandoned checkouts"
    />
  );
}