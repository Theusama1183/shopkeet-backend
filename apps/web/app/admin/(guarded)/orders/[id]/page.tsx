import Link from "next/link";
import { notFound } from "next/navigation";
import { DownloadIcon } from "lucide-react";

import { adminRequest } from "@/lib/admin-api";
import type { AdminOrder } from "@/lib/admin-types";
import { formatMoney, formatDateTime } from "@/lib/format";
import { paymentTone, label, PAYMENT_LABELS } from "@/lib/statuses";
import { PageHeader } from "@/components/admin/page-header";
import { OrderStatusEditor, OrderNoteEditor } from "@/components/admin/order-status-editor";
import { StatusBadge } from "@/components/admin/status-badge";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

export default async function OrderDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  let order: AdminOrder;
  try {
    order = await adminRequest<AdminOrder>(`/orders/${id}`);
  } catch (error) {
    if (error instanceof Error && "status" in error && (error as { status: number }).status === 404) {
      notFound();
    }
    throw error;
  }

  const lineCount = order.items.reduce((sum, item) => sum + item.quantity, 0);
  const subtotal = order.items.reduce((sum, item) => sum + item.line_total_cents, 0);
  const shippingName = [order.shipping_method, order.tracking_number && `#${order.tracking_number}`]
    .filter(Boolean)
    .join(", ");

  return (
    <>
      <PageHeader
        crumbs={[{ label: "Orders", href: "/admin/orders" }, { label: `#${order.id.slice(0, 8)}` }]}
        title={`#${order.id.slice(0, 8)}`}
        description={`Placed ${formatDateTime(order.created_at)} (${lineCount} item${lineCount === 1 ? "" : "s"})`}
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link href={`/api/admin/orders/${order.id}/invoice.pdf`}>
              <DownloadIcon aria-hidden="true" />
              Invoice
            </Link>
          </Button>
        }
      />

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          <Card>
            <CardHeader>
              <CardTitle>Items</CardTitle>
            </CardHeader>
            <CardContent>
              <ul className="divide-y divide-border">
                {order.items.map((item) => (
                  <li key={item.id} className="flex items-baseline justify-between gap-4 py-3 first:pt-0 last:pb-0">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium text-foreground">
                        {item.variant_id.slice(0, 8)}
                        <span className="ml-2 text-muted-foreground">× {item.quantity}</span>
                      </p>
                      <p className="text-caption text-muted-foreground">Variant {item.variant_id.slice(0, 8)}</p>
                    </div>
                    <span className="shrink-0 text-sm tabular-nums text-foreground">
                      {formatMoney(item.line_total_cents, order.currency)}
                    </span>
                  </li>
                ))}
              </ul>

              <Separator className="my-4" />

              <dl className="space-y-1.5 text-sm">
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Subtotal</dt>
                  <dd className="tabular-nums">{formatMoney(subtotal, order.currency)}</dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Shipping</dt>
                  <dd className="tabular-nums">{formatMoney(order.shipping_cost_cents, order.currency)}</dd>
                </div>
                {order.discount_cents > 0 ? (
                  <div className="flex justify-between">
                    <dt className="text-muted-foreground">
                      Discount{order.discount_code ? ` (${order.discount_code})` : ""}
                    </dt>
                    <dd className="tabular-nums text-muted-foreground">−{formatMoney(order.discount_cents, order.currency)}</dd>
                  </div>
                ) : null}
                {order.gift_card_cents > 0 ? (
                  <div className="flex justify-between">
                    <dt className="text-muted-foreground">Gift card</dt>
                    <dd className="tabular-nums text-muted-foreground">−{formatMoney(order.gift_card_cents, order.currency)}</dd>
                  </div>
                ) : null}
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Tax</dt>
                  <dd className="tabular-nums">{formatMoney(order.tax_cents, order.currency)}</dd>
                </div>
              </dl>

              <Separator className="my-4" />

              <div className="flex items-center justify-between">
                <span className="text-sm font-medium">Total paid</span>
                <span className="text-lg font-semibold tabular-nums">
                  {formatMoney(order.total_cents, order.currency)}
                </span>
              </div>
            </CardContent>
          </Card>

          <div className="grid gap-4 sm:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>Customer</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1 text-sm">
                <p className="font-medium text-foreground">{order.customer_name}</p>
                <p className="text-muted-foreground">{order.customer_phone}</p>
                {order.customer_email ? (
                  <p className="text-muted-foreground break-all">{order.customer_email}</p>
                ) : null}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Shipping</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1 text-sm">
                {order.shipping_address_line1 ? (
                  <>
                    <p className="text-foreground">{order.shipping_address_line1}</p>
                    {order.shipping_address_line2 ? <p className="text-muted-foreground">{order.shipping_address_line2}</p> : null}
                    <p className="text-muted-foreground">
                      {[order.shipping_city, order.shipping_state, order.shipping_postal_code].filter(Boolean).join(", ")}
                    </p>
                    <p className="text-muted-foreground">{order.shipping_country}</p>
                  </>
                ) : (
                  <p className="text-caption text-muted-foreground">No address record</p>
                )}
                {shippingName ? <p className="pt-1 text-muted-foreground">{shippingName}</p> : null}
                {order.tracking_url ? (
                  <Button variant="link" size="sm" className="h-auto p-0" asChild>
                    <Link href={order.tracking_url} target="_blank" rel="noreferrer">
                      Track shipment
                    </Link>
                  </Button>
                ) : null}
              </CardContent>
            </Card>
          </div>
        </div>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Fulfilment</CardTitle>
              <CardDescription>Advance the order along its lifecycle.</CardDescription>
            </CardHeader>
            <CardContent>
              <OrderStatusEditor
                orderId={order.id}
                status={order.status}
                paymentStatus={order.payment_status}
                trackingNumber={order.tracking_number}
                trackingCarrier={order.tracking_carrier}
                trackingUrl={order.tracking_url}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex items-center justify-between space-y-0">
              <CardTitle>Order details</CardTitle>
              <StatusBadge tone={paymentTone(order.payment_status)}>
                {label(PAYMENT_LABELS, order.payment_status)}
              </StatusBadge>
            </CardHeader>
            <CardContent>
              <dl className="space-y-1.5 text-sm">
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Payment method</dt>
                  <dd className="text-foreground">{order.payment_method || "—"}</dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Source</dt>
                  <dd className="text-foreground">{order.source}</dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-muted-foreground">Tracking number</dt>
                  <dd className="text-foreground">{order.tracking_number || "—"}</dd>
                </div>
              </dl>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Notes</CardTitle>
            </CardHeader>
            <CardContent>
              <OrderNoteEditor orderId={order.id} note={order.internal_note} />
            </CardContent>
          </Card>
        </div>
      </div>
    </>
  );
}