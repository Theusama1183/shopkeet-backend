import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Discounts" };

export default function DiscountsPage() {
  return (
    <ComingSoon
      title="Discounts"
      description="Create promo codes, gift cards, and automatic discounts."
    />
  );
}