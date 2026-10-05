import type { Metadata } from "next";
import Link from "next/link";

import { PlusIcon } from "lucide-react";

import { adminRequest } from "@/lib/admin-api";
import type { AdminOrdersResponse, AbandonedCartsResponse } from "@/lib/admin-types";
import { PageHeader } from "@/components/admin/page-header";
import { OrdersViews, type OrdersView } from "@/components/admin/orders-views";
import { OrdersListView } from "@/components/admin/orders-list-view";
import { AbandonedTable } from "@/components/admin/abandoned-table";
import { Button } from "@/components/ui/button";

export const metadata: Metadata = { title: "Orders" };

interface OrdersPageProps {
  searchParams: Promise<{
    view?: string;
    status?: string;
    payment_status?: string;
  }>;
}

function resolveView(raw: string | undefined): OrdersView {
  if (raw === "drafts" || raw === "abandoned") return raw;
  return "all";
}

export default async function OrdersPage({ searchParams }: OrdersPageProps) {
  const { status, payment_status } = await searchParams;
  const view = resolveView((await searchParams).view);

  if (view === "abandoned") {
    const data = await adminRequest<AbandonedCartsResponse>("/carts/abandoned");
    return (
      <>
        <PageHeader
          title="Orders"
          description="Abandoned checkouts — shoppers who entered an email but never completed payment."
          actions={<OrdersViews current={view} />}
        />
        <AbandonedTable carts={data.carts} />
      </>
    );
  }

  const isDrafts = view === "drafts";
  const query = new URLSearchParams();
  if (isDrafts) {
    query.set("source", "draft");
  } else {
    if (status) query.set("status", status);
    if (payment_status) query.set("payment_status", payment_status);
  }
  const data = await adminRequest<AdminOrdersResponse>(`/orders${query.toString() ? `?${query.toString()}` : ""}`);

  return (
    <>
      <PageHeader
        title="Orders"
        description={isDrafts ? "Sales you created for a phone or in-person order." : "Every order across your store."}
        actions={
          <>
            <OrdersViews current={view} />
            {isDrafts ? (
              <Button size="sm" asChild>
                <Link href="/admin/orders/drafts/new">
                  <PlusIcon aria-hidden="true" />
                  New draft
                </Link>
              </Button>
            ) : null}
          </>
        }
      />
      <OrdersListView orders={data.orders} />
    </>
  );
}