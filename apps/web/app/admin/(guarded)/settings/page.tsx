import type { Metadata } from "next";

import { ComingSoon } from "@/components/admin/coming-soon";

export const metadata: Metadata = { title: "Settings" };

export default function SettingsPage() {
  return (
    <ComingSoon
      title="Settings"
      description="Store profile, shipping zones, tax, users, and integrations."
    />
  );
}