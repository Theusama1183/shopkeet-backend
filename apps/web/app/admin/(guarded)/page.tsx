import type { Metadata } from "next";
import Link from "next/link";
import { ChevronRightIcon, FileTextIcon, HomeIcon, PackagePlusIcon, SettingsIcon } from "lucide-react";

import { adminRequest } from "@/lib/admin-api";
import { formatMoney, formatDate, formatPercent } from "@/lib/format";
import type {
  SalesAnalytics,
  TopProductsAnalytics,
  ConversionAnalytics,
  AdminOrdersResponse,
  AdminProductsResponse,
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

const FIRST_RUN_STEPS = [
  {
    href: "/admin/products",
    title: "Add your first product",
    blurb: "Sell something you make or source — name it, price it, go live.",
    icon: PackagePlusIcon,
  },
  {
    href: "/admin/content",
    title: "Customize your storefront",
    blurb: "Tweak pages, copy and the feel of your shop before you share it.",
    icon: FileTextIcon,
  },
  {
    href: "/admin/settings",
    title: "Confirm shipping & payments",
    blurb: "Set rates and make sure cash-on-delivery is ready to take orders.",
    icon: SettingsIcon,
  },
];

/**
 * Home is hierarchy-first: one hero number (total sales for the period) sits
 * directly on the canvas, secondary metrics cluster beside it at a fraction of
 * its weight, and the chart is the only thing on the page worth a strong box.
 * No row of identical stat cards — that shape is what makes dashboards read as
 * generated rather than designed (docs/05).
 *
 * A store with nothing to report yet (no products, no orders) gets a welcome
 * panel and a three-step checklist instead of a wall of zeroes.
 */
export default async function HomePage({ searchParams }: HomePageProps) {
  const { period = "30d" } = await searchParams;

  const q = `?period=${period}`;
  const [sales, topProducts, conversion, orders, products] = await Promise.all([
    adminRequest<SalesAnalytics>(`/analytics/sales${q}`),
    adminRequest<TopProductsAnalytics>(`/analytics/top-products${q}&limit=5`),
    adminRequest<ConversionAnalytics>(`/analytics/conversion${q}`),
    adminRequest<AdminOrdersResponse>("/orders"),
    adminRequest<AdminProductsResponse>("/products"),
  ]);

  const isFirstRun = products.products.length === 0 && orders.orders.length === 0;

  if (isFirstRun) {
    return (
      <>
        <PageHeader
          title="Home"
          icon={HomeIcon}
          description="How your store is doing."
          actions={<PeriodSwitcher />}
        />

        <section>
          <h2 className="text-h2 font-semibold tracking-tight text-foreground">
            Welcome to your new store
          </h2>
          <p className="mt-1 max-w-xl text-body text-muted-foreground">
            Nothing to report yet — this canvas is yours. Three steps and you can
            take your first order.
          </p>

          <ol className="mt-6 divide-y divide-border border-y border-border">
            {FIRST_RUN_STEPS.map((step) => (
              <li key={step.href}>
                <Link
                  href={step.href}
                  className="group flex items-center gap-4 py-4 transition-colors hover:bg-card"
                >
                  <step.icon className="size-5 shrink-0 text-primary" aria-hidden="true" />
                  <div className="min-w-0 flex-1">
                    <p className="text-body font-medium text-foreground">{step.title}</p>
                    <p className="mt-0.5 text-caption text-muted-foreground">{step.blurb}</p>
                  </div>
                  <ChevronRightIcon
                    className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5"
                    aria-hidden="true"
                  />
                </Link>
              </li>
            ))}
          </ol>
        </section>
      </>
    );
  }

  const recent = orders.orders.slice(0, 5);
  const currency = sales.currency;
  const units = formatMoney(sales.totals.revenue_cents, currency);
  const orderCount = sales.totals.order_count;
  const top = topProducts.items[0];

  return (
    <>
      <PageHeader
        title="Home"
        icon={HomeIcon}
        description="How your store is doing."
        actions={<PeriodSwitcher />}
      />

      <section className="flex flex-col gap-5 lg:flex-row lg:items-stretch lg:gap-8">
        <div className="min-w-0 shrink-0">
          <p className="text-label font-medium text-muted-foreground">Total sales</p>
          <p className="mt-1.5 text-display font-semibold tracking-tight tabular-nums text-foreground">
            {units}
          </p>
          <p className="mt-1.5 text-label text-muted-foreground">
            {orderCount} order{orderCount === 1 ? "" : "s"} in the last {period}
          </p>
        </div>

        <div className="hidden w-px bg-border lg:block" aria-hidden="true" />

        <div className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-3 lg:flex lg:flex-1 lg:items-center lg:gap-8">
          <StatCard
            label="Orders"
            value={String(orderCount)}
            hint={`last ${period}`}
            className="lg:min-w-0 lg:flex-1"
          />
          <StatCard
            label="Conversion"
            value={formatPercent(conversion.conversion_rate)}
            hint={`${conversion.orders_placed} of ${conversion.carts_created} carts`}
            className="lg:min-w-0 lg:flex-1"
          />
          <StatCard
            label="Top product"
            value={top?.product_name ?? "—"}
            hint={top ? `${top.quantity} sold` : "no sales yet"}
            className="col-span-2 sm:col-span-1 lg:min-w-0 lg:flex-1"
          />
        </div>
      </section>

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <SalesChart buckets={sales.buckets} currency={currency} conversionRate={conversion.conversion_rate} />
        </div>

        {/* Top products sit on the canvas — the chart is the only strong box. */}
        <section className="lg:min-w-0">
          <div className="mb-3 flex items-center justify-between">
            <h2 className="text-h3 font-medium tracking-tight text-foreground">Top products</h2>
            <span className="text-caption text-muted-foreground">{period}</span>
          </div>
          <ul className="divide-y divide-border border-y border-border">
            {topProducts.items.length === 0 ? (
              <li className="py-3 text-body text-muted-foreground">Nothing sold yet in this window.</li>
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
        </section>
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
                    className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 transition-colors hover:bg-muted"
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