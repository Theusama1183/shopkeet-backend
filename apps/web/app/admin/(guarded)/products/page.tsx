import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Products" };

export default function ProductsPage() {
  return (
    <ComingSoon
      title="Products"
      description="Manage your catalog — add products, variants, inventory, and pricing."
    />
  );
}