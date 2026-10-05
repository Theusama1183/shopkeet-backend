"use client";

import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { formatMoney, formatPercent } from "@/lib/format";
import type { SalesBucket } from "@/lib/admin-types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function SalesChart({
  buckets,
  currency,
  conversionRate,
}: {
  buckets: SalesBucket[];
  currency: string;
  conversionRate: number;
}) {
  const data = buckets.map((bucket) => ({
    ...bucket,
    raw_date: bucket.date,
    date: new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric" }).format(new Date(bucket.date)),
  }));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-h3">Sales over time</CardTitle>
      </CardHeader>
      <CardContent className="h-72 p-4">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={data} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="salesFill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--primary)" stopOpacity={0.25} />
                <stop offset="100%" stopColor="var(--primary)" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
            <XAxis
              dataKey="date"
              tick={{ fontSize: 12, fill: "var(--muted-foreground)" }}
              tickLine={false}
              axisLine={false}
              minTickGap={24}
            />
            <YAxis
              tick={{ fontSize: 12, fill: "var(--muted-foreground)" }}
              tickLine={false}
              axisLine={false}
              width={52}
              tickFormatter={(value: number) => formatMoney(value, currency).replace(/\.00$/, "")}
            />
            <Tooltip
              formatter={(value) => formatMoney(Number(value), currency)}
              labelFormatter={(_, payload) => {
                const raw = payload?.[0]?.payload?.raw_date as string | undefined;
                if (!raw) return "";
                return new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", year: "numeric" }).format(new Date(raw));
              }}
              contentStyle={{ background: "var(--popover)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 13 }}
            />
            <Area
              type="monotone"
              dataKey="revenue_cents"
              stroke="var(--primary)"
              strokeWidth={2}
              fill="url(#salesFill)"
            />
          </AreaChart>
        </ResponsiveContainer>
        <p className="mt-1 text-caption text-muted-foreground">
          {data.length === 0
            ? "No orders in this window yet."
            : `${formatPercent(conversionRate)} of carts turned into orders in this window.`}
        </p>
      </CardContent>
    </Card>
  );
}