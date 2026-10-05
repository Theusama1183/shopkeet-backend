import type { Metadata } from "next";
import Link from "next/link";

import {
  BanknoteIcon,
  PackageIcon,
  ShoppingCartIcon,
  ShoppingBagIcon,
} from "lucide-react";

import { adminRequest } from "@/lib/admin-api";
import { formatMoney, formatDate, formatPercent } from "@/lib/format";
import type {
  SalesAnalytics,
  TopProductsAnalytics,
  ConversionAnalytics,
  AdminOrdersResponse,
} from "@/lib/admin-types";
import { PageHeader } from "@/components/admin/page-header";
import { StatCard } from "@/components/admin/stat-card";
import { StatusBadge } from "@/components/admin/status-badge";
import { PeriodSwitcher } from "@/components/admin/period-switcher";
import { SalesChart } from "@/components/admin/sales-chart";
import { orderTone, paymentTone, label, ORDER_LABELS, PAYMENT_LABELS } from "@/lib/statuses";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

export const metadata: Metadata = { title: "Home" };

interface HomePageProps {
  searchParams: Promise<{ period?: string }>;
}

export default async function HomePage({ searchParams }: HomePageProps) {
  const { period = "30d" } = await searchParams;

  const q = `?period=${period}`;
  const [sales, topProducts, conversion, orders] = await Promise.all([
    adminRequest<SalesAnalytics>(`/analytics/sales${q}`),
    adminRequest<TopProductsAnalytics>(`/analytics/top-products${q}&limit=5`),
    adminRequest<ConversionAnalytics>(`/analytics/conversion${q}`),
    adminRequest<AdminOrdersResponse>("/orders"),
  ]);

  const recent = orders.orders.slice(0, 5);
  const currency = sales.currency;
  const units = formatMoney(sales.totals.revenue_cents, currency);

  return (
    <>
      <PageHeader
        title="Home"
        description="How your store is doing."
        actions={<PeriodSwitcher />}
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Total sales"
          value={units}
          hint={`${sales.totals.order_count} order${sales.totals.order_count === 1 ? "" : "s"}`}
          trend={{ direction: "up", label: period, tone: "success" }}
          icon={BanknoteIcon}
        />
        <StatCard
          label="Orders"
          value={String(sales.totals.order_count)}
          hint={`in the last ${period}`}
          icon={ShoppingBagIcon}
        />
        <StatCard
          label="Conversion"
          value={formatPercent(conversion.conversion_rate)}
          hint={`${conversion.orders_placed} of ${conversion.carts_created} carts`}
          trend={{ direction: conversion.conversion_rate >= 0.1 ? "up" : "down", label: "carts → orders", tone: conversion.conversion_rate >= 0.1 ? "success" : "neutral" }}
          icon={ShoppingCartIcon}
        />
        <StatCard
          label="Top product"
          value={topProducts.items[0]?.product_name ?? "—"}
          hint={topProducts.items[0] ? `${topProducts.items[0].quantity} sold` : "no sales yet"}
          icon={PackageIcon}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <SalesChart buckets={sales.buckets} currency={currency} conversionRate={conversion.conversion_rate} />
        </div>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle className="text-h3">Top products</CardTitle>
            <span className="text-caption text-muted-foreground">{period}</span>
          </CardHeader>
          <CardContent className="p-4">
            <ul className="divide-y divide-border">
              {topProducts.items.length === 0 ? (
                <li className="text-body text-muted-foreground">Nothing sold yet in this window.</li>
              ) : (
                topProducts.items.map((item, index) => (
                  <li key={item.product_id} className="flex items-center gap-3 py-2.5">
                    <span className="w-5 text-caption text-muted-foreground tabular-nums">{index + 1}</span>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-body font-medium text-foreground">{item.product_name}</p>
                      <p className="text-caption text-muted-foreground">{item.quantity} sold</p>
                    </div>
                    <span className="text-label font-medium text-foreground tabular-nums">
                      {formatMoney(item.revenue_cents, currency)}
                    </span>
                  </li>
                ))
              )}
            </ul>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="text-h3">Recent orders</CardTitle>
          <Button variant="ghost" size="sm" asChild>
            <Link href="/admin/orders">View all</Link>
          </Button>
        </CardHeader>
        <CardContent className="p-0">
          {recent.length === 0 ? (
            <p className="px-4 pb-4 text-body text-muted-foreground">
              No orders yet. Share your storefront or create a draft order for a phone sale.
            </p>
          ) : (
            <ul className="divide-y divide-border">
              {recent.map((order) => (
                <li key={order.id}>
                  <Link
                    href={`/admin/orders/${order.id}`}
                    className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 transition-colors hover:bg-muted/40"
                  >
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-body font-medium text-foreground">{order.customer_name}</p>
                      <p className="text-caption text-muted-foreground">{formatDate(order.created_at)}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      <StatusBadge tone={orderTone(order.status)}>{label(ORDER_LABELS, order.status)}</StatusBadge>
                      <StatusBadge tone={paymentTone(order.payment_status)}>{label(PAYMENT_LABELS, order.payment_status)}</StatusBadge>
                    </div>
                    <span className="min-w-20 text-right text-label font-medium text-foreground tabular-nums">
                      {formatMoney(order.total_cents, order.currency)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </>
  );
}