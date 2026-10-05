import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Growth" };

export default function GrowthPage() {
  return (
    <ComingSoon
      title="Growth"
      description="Lead capture, reviews, loyalty, and affiliate program."
    />
  );
}