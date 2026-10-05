"use client";

import { useRouter } from "next/navigation";

import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

export type OrdersView = "all" | "drafts" | "abandoned";

const VIEWS: { value: OrdersView; label: string; path: string }[] = [
  { value: "all", label: "All orders", path: "/admin/orders" },
  { value: "drafts", label: "Drafts", path: "/admin/orders?view=drafts" },
  { value: "abandoned", label: "Abandoned checkouts", path: "/admin/orders?view=abandoned" },
];

/** Tab row: All / Drafts / Abandoned checkouts, driven by ?view=. */
export function OrdersViews({ current }: { current: OrdersView }) {
  const router = useRouter();

  return (
    <Tabs
      value={current}
      onValueChange={(value) => {
        const view = VIEWS.find((v) => v.value === value);
        if (view) router.push(view.path);
      }}
      aria-label="Orders views"
    >
      <TabsList>
        {VIEWS.map((view) => (
          <TabsTrigger key={view.value} value={view.value}>
            {view.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  );
}