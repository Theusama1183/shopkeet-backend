"use client";

import Link from "next/link";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";

import { formatMoney, formatDate } from "@/lib/format";
import type { AdminOrder } from "@/lib/admin-types";
import { orderTone, paymentTone, label, ORDER_LABELS, PAYMENT_LABELS } from "@/lib/statuses";
import { DataTable } from "@/components/admin/data-table";
import { StatusBadge } from "@/components/admin/status-badge";

export function OrdersTable({
  orders,
  showSource = false,
  query = "",
}: {
  orders: AdminOrder[];
  showSource?: boolean;
  query?: string;
}) {
  const columns: LegacyColumnDef<AdminOrder, unknown>[] = [
    {
      id: "order",
      header: "Order",
      enableSorting: true,
      cell: ({ row }) => (
        <Link href={`/admin/orders/${row.original.id}`} className="font-medium text-primary hover:underline">
          #{row.original.id.slice(0, 8)}
        </Link>
      ),
    },
    {
      id: "customer",
      header: "Customer",
      enableSorting: true,
      cell: ({ row }) => (
        <div className="min-w-0">
          <p className="truncate font-medium text-foreground">{row.original.customer_name}</p>
          <p className="truncate text-caption text-muted-foreground">{row.original.customer_phone || "—"}</p>
        </div>
      ),
    },
    {
      id: "date",
      header: "Date",
      enableSorting: true,
      cell: ({ row }) => <span className="text-muted-foreground">{formatDate(row.original.created_at)}</span>,
    },
    {
      id: "items",
      header: "Items",
      accessorFn: (order) => order.items.reduce((sum, item) => sum + item.quantity, 0),
      cell: ({ row }) => (
        <span className="tabular-nums text-muted-foreground">
          {row.original.items.reduce((sum, item) => sum + item.quantity, 0)}
        </span>
      ),
    },
    {
      id: "total",
      header: "Total",
      accessorFn: (order) => order.total_cents,
      cell: ({ row }) => (
        <span className="font-medium text-foreground tabular-nums">{formatMoney(row.original.total_cents, row.original.currency)}</span>
      ),
    },
    {
      id: "payment",
      header: "Payment",
      cell: ({ row }) => (
        <StatusBadge tone={paymentTone(row.original.payment_status)}>
          {label(PAYMENT_LABELS, row.original.payment_status)}
        </StatusBadge>
      ),
    },
    {
      id: "status",
      header: "Status",
      cell: ({ row }) => (
        <StatusBadge tone={orderTone(row.original.status)}>{label(ORDER_LABELS, row.original.status)}</StatusBadge>
      ),
    },
  ];

  if (showSource) {
    columns.push({
      id: "source",
      header: "Source",
      cell: ({ row }) => (
        <span className={row.original.source === "draft" ? "text-caption text-primary" : "text-caption text-muted-foreground"}>
          {row.original.source}
        </span>
      ),
    });
  }

  return (
    <DataTable
      columns={columns}
      data={orders}
      getRowId={(order) => order.id}
      query={query}
      queryFilter={(order, value) => {
        const haystack = [order.id, order.customer_name, order.customer_phone, order.customer_email]
          .join(" ")
          .toLowerCase();
        return haystack.includes(value.toLowerCase());
      }}
      emptyTitle={showSource ? "No drafts yet" : "No orders yet"}
      emptyDescription={
        showSource
          ? "Create a draft for a phone or in-person sale and it'll show up here."
          : "Orders placed on your storefront will appear here."
      }
      aria-label="Orders"
    />
  );
}