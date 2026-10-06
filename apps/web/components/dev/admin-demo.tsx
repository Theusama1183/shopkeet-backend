"use client"

import { useMemo, useState } from "react"
import { toast } from "sonner"
import {
  PackagePlusIcon,
  ArchiveIcon,
  Trash2Icon,
} from "lucide-react"
import { legacyCreateColumnHelper } from "@tanstack/react-table/legacy"

import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/admin/page-header"
import { StatCard } from "@/components/admin/stat-card"
import { FilterBar } from "@/components/admin/filter-bar"
import { DataTable } from "@/components/admin/data-table"
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge"
import { formatCurrency } from "@/lib/formats"

interface DemoProduct {
  id: string
  name: string
  category: string
  priceCents: number
  status: "active" | "low-stock" | "draft" | "archived"
}

const PRODUCT_NAMES: Array<[string, string]> = [
  ["Stoneware mug", "Kitchen"],
  ["Linen tea towel", "Kitchen"],
  ["Ceramic pouring bowl", "Kitchen"],
  ["Maple cutting board", "Kitchen"],
  ["Enamel camping cup", "Kitchen"],
  ["Wool throw blanket", "Home goods"],
  ["Candle, cedar", "Home goods"],
  ["Dried flower bunch", "Home goods"],
  ["Glass carafe", "Kitchen"],
  ["Cotton market bag", "Home goods"],
  ["Pocket notebook", "Stationery"],
  ["Ink pen, brass", "Stationery"],
  ["Wax seal set", "Stationery"],
  ["Desk blotter", "Stationery"],
  ["Washi tape trio", "Stationery"],
  ["Hemp tote", "Apparel"],
  ["Recycled hoodie", "Apparel"],
  ["Wool beanie", "Apparel"],
  ["Canvas apron", "Apparel"],
  ["Boiled wool scarf", "Apparel"],
  ["Balm, unscented", "Body care"],
  ["Soap bar, oak", "Body care"],
  ["Lip tint, clay", "Body care"],
  ["Hair oil dropper", "Body care"],
]

const STATUSES: DemoProduct["status"][] = ["active", "active", "active", "low-stock", "draft", "archived"]

function buildProducts(): DemoProduct[] {
  return Array.from({ length: 42 }, (_, i) => {
    const [name, category] = PRODUCT_NAMES[i % PRODUCT_NAMES.length]
    return {
      id: `pd_${String(i + 1).padStart(4, "0")}`,
      name: name + (i >= PRODUCT_NAMES.length ? ` ${Math.floor(i / PRODUCT_NAMES.length) + 1}` : ""),
      category,
      priceCents: 1200 + ((i * 371) % 88) * 25,
      status: STATUSES[i % STATUSES.length],
    }
  })
}

const STATUS_TONES: Record<DemoProduct["status"], StatusTone> = {
  active: "success",
  "low-stock": "warning",
  draft: "neutral",
  archived: "neutral",
}

const STATUS_LABELS: Record<DemoProduct["status"], string> = {
  active: "Active",
  "low-stock": "Low stock",
  draft: "Draft",
  archived: "Archived",
}

const columnHelper = legacyCreateColumnHelper<DemoProduct>()

export function AdminDemo() {
  const [query, setQuery] = useState("")
  const [loading, setLoading] = useState(false)
  const data = useMemo(() => buildProducts(), [])

  const columns = useMemo(
    () => [
      columnHelper.accessor("name", {
        header: "Product",
        cell: (info) => (
          <span className="font-medium text-foreground">{info.getValue()}</span>
        ),
      }),
      columnHelper.accessor("category", { header: "Category" }),
      columnHelper.accessor("priceCents", {
        header: "Price",
        cell: (info) => formatCurrency(info.getValue()),
      }),
      columnHelper.accessor("status", {
        header: "Status",
        cell: (info) => (
          <StatusBadge tone={STATUS_TONES[info.getValue()]}>
            {STATUS_LABELS[info.getValue()]}
          </StatusBadge>
        ),
      }),
    ],
    []
  )

  return (
    <div className="grid gap-6 rounded-lg border border-border bg-card p-6">
      <PageHeader
        crumbs={[{ label: "Products" }]}
        title="Products"
        description="Everything you sell. Tables are the primary UI for lists like this."
        actions={
          <Button onClick={() => toast.info("The catalog screen lands next.")}>
            <PackagePlusIcon /> Add product
          </Button>
        }
      />
      <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="Revenue (30d)"
          value={formatCurrency(482900)}
          trend={{ direction: "up", label: "12%", tone: "success" }}
        />
        <StatCard
          label="Orders (30d)"
          value="214"
          trend={{ direction: "up", label: "8%", tone: "success" }}
        />
        <StatCard
          label="Customers"
          value="1,029"
          trend={{ direction: "up", label: "3.2%", tone: "success" }}
        />
        <StatCard
          label="Low-stock items"
          value="6"
          trend={{ direction: "down", label: "2 resolved", tone: "success" }}
        />
      </div>
      <FilterBar
        search={query}
        onSearchChange={setQuery}
        searchPlaceholder="Search products…"
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => setLoading((value) => !value)}>
              {loading ? "Show data" : "Simulate loading"}
            </Button>
          </>
        }
      />
      <div>
        <DataTable
          columns={columns}
          data={data}
          getRowId={(row) => row.id}
          loading={loading}
          selectable
          query={query}
          queryFilter={(row, value) =>
            row.name.toLowerCase().includes(value) || row.category.toLowerCase().includes(value)
          }
          bulkActions={(selected) => (
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={() =>
                  toast.info(`Archived ${selected.map((row) => row.name).join(", ")}`)
                }
              >
                <ArchiveIcon /> Archive
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => toast.info(`Deleted ${selected.length} product(s)`)}
              >
                <Trash2Icon /> Delete
              </Button>
            </>
          )}
          emptyTitle="Add your first product"
          emptyDescription="Products appear here once you publish your inventory."
          emptyAction={
            <Button size="sm" onClick={() => toast.info("Add product screen is next.")}>
              Add a product
            </Button>
          }
          aria-label="Products"
        />
      </div>
    </div>
  )
}