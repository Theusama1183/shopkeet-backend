"use client";

import { useRouter, useSearchParams } from "next/navigation";

import { FilterBar } from "@/components/admin/filter-bar";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const ORDER_STATUSES = ["pending", "confirmed", "shipped", "delivered", "cancelled"];
const PAYMENT_STATUSES = ["pending", "paid", "refunded", "failed"];

/** Sentinel select value that clears the URL param. */
const clear = "__all__";

/**
 * The orders filter bar. Status/payment selects write to the URL (?status= &
 * ?payment_status=) so a server render owns the data — the browser never holds
 * the JWT, so client-side refetching isn't an option on admin reads. Search is
 * a local, client-side filter over the loaded page (the Go API has no order
 * text search).
 */
export function OrdersFilters({
  search,
  onSearchChange,
}: {
  search: string;
  onSearchChange: (value: string) => void;
}) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const status = searchParams.get("status") ?? "";
  const paymentStatus = searchParams.get("payment_status") ?? "";

  const update = (next: Record<string, string>) => {
    const params = new URLSearchParams(searchParams.toString());
    for (const [key, value] of Object.entries(next)) {
      const resolved = value === clear ? "" : value;
      if (resolved) params.set(key, resolved);
      else params.delete(key);
    }
    const queryString = params.toString();
    router.replace(`/admin/orders${queryString ? `?${queryString}` : ""}`);
  };

  return (
    <FilterBar
      search={search}
      onSearchChange={onSearchChange}
      searchPlaceholder="Search customer, phone, or order…"
      fields={[
        {
          id: "status",
          label: "Status",
          control: (
            <Select value={status} onValueChange={(value) => update({ status: value })}>
              <SelectTrigger size="sm" className="w-36" aria-label="Order status">
                <SelectValue placeholder="Any status" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={clear}>Any status</SelectItem>
                {ORDER_STATUSES.map((option) => (
                  <SelectItem key={option} value={option}>
                    {option}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ),
        },
        {
          id: "payment",
          label: "Payment",
          control: (
            <Select value={paymentStatus} onValueChange={(value) => update({ payment_status: value })}>
              <SelectTrigger size="sm" className="w-32" aria-label="Payment status">
                <SelectValue placeholder="Any payment" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={clear}>Any payment</SelectItem>
                {PAYMENT_STATUSES.map((option) => (
                  <SelectItem key={option} value={option}>
                    {option}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ),
        },
      ]}
    />
  );
}