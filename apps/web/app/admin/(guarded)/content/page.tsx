import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Content" };

export default function ContentPage() {
  return (
    <ComingSoon
      title="Content"
      description="Blog posts, pages, and the storefront design built with the page builder."
    />
  );
}