import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Customers" };

export default function CustomersPage() {
  return (
    <ComingSoon
      title="Customers"
      description="Everyone who's bought from you, their order history, and their tags."
    />
  );
}