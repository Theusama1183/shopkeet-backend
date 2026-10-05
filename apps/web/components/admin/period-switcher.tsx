"use client";

import { useRouter, useSearchParams } from "next/navigation";
import {
  Tabs,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";

export const PERIODS = [
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
] as const;

/**
 * Drives every analytics read on the dashboard through the URL (?period=30d),
 * so a server render (not client state) owns the data. Defaults to 30d, the
 * API's own default.
 */
export function PeriodSwitcher() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const period = searchParams.get("period") ?? "30d";

  const select = (value: string) => {
    const params = new URLSearchParams(searchParams.toString());
    params.set("period", value);
    router.replace(`/admin?${params.toString()}`);
  };

  return (
    <Tabs value={period} onValueChange={select} aria-label="Analytics period">
      <TabsList>
        {PERIODS.map((p) => (
          <TabsTrigger key={p.value} value={p.value}>
            {p.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  );
}