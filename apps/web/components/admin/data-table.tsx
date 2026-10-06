"use client"

import * as React from "react"
import {
  ArrowDownIcon,
  ArrowUpIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  ChevronsUpDownIcon,
} from "lucide-react"

import { cn } from "cn"
import {
  getCoreRowModel,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useLegacyTable,
  type LegacyColumnDef,
} from "@tanstack/react-table/legacy"
import { flexRender } from "@tanstack/react-table"
import type { PaginationState, SortingState } from "@tanstack/table-core"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { EmptyState } from "@/components/feedback/empty-state"
import { Skeleton } from "@/components/ui/skeleton"

export interface DataTableProps<TData extends object> {
  /* eslint-disable-next-line @typescript-eslint/no-explicit-any -- column value types vary per column; the table stays generic over them */
  columns: LegacyColumnDef<TData, any>[]
  data: TData[]
  getRowId?: (row: TData) => string
  /** Loading state — renders a skeleton table of `skeletonRows` rows. */
  loading?: boolean
  skeletonRows?: number
  /** Adds a selection checkbox column and enables row selection. */
  selectable?: boolean
  /** Rendered (sticky) once rows are selected — the bulk-action bar. */
  bulkActions?: (selected: TData[]) => React.ReactNode
  /** External query from a FilterBar; combined with `queryFilter`. */
  query?: string
  queryFilter?: (row: TData, query: string) => boolean
  initialPageSize?: number
  emptyTitle?: string
  emptyDescription?: string
  emptyAction?: React.ReactNode
  className?: string
  "aria-label"?: string
}

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

export function DataTable<TData extends object>({
  columns,
  data,
  getRowId,
  loading = false,
  skeletonRows = 6,
  selectable = false,
  bulkActions,
  query = "",
  queryFilter,
  initialPageSize = 20,
  emptyTitle,
  emptyDescription,
  emptyAction,
  className,
  "aria-label": ariaLabel,
}: DataTableProps<TData>) {
  const [sorting, setSorting] = React.useState<SortingState>([])
  const [rowSelection, setRowSelection] = React.useState({})
  const [pagination, setPagination] = React.useState<PaginationState>({
    pageIndex: 0,
    pageSize: initialPageSize,
  })

  const filteredData = React.useMemo(() => {
    const trimmed = query.trim()
    if (!trimmed || !queryFilter) return data
    return data.filter((row) => queryFilter(row, trimmed))
  }, [data, query, queryFilter])

  const table = useLegacyTable<TData>({
    data: filteredData,
    columns,
    getRowId,
    state: {
      sorting,
      rowSelection,
      pagination,
    },
    onSortingChange: setSorting,
    onRowSelectionChange: setRowSelection,
    onPaginationChange: setPagination,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    enableRowSelection: selectable,
  })

  const isEmpty = !loading && filteredData.length === 0
  const noResults = !loading && data.length > 0 && filteredData.length === 0
  const selectedRows = table.getSelectedRowModel().rows.map((row) => row.original)
  const canPreviousPage = table.getCanPreviousPage()
  const canNextPage = table.getCanNextPage()
  const { pageSize, pageIndex } = pagination
  const rows = table.getRowModel().rows

  const pageStart = filteredData.length === 0 ? 0 : pageIndex * pageSize + 1
  const pageEnd = Math.min((pageIndex + 1) * pageSize, filteredData.length)

  return (
    <div
      className={cn(
        "overflow-hidden rounded-xl border border-border bg-card shadow-[0_1px_0_0_rgba(0,0,0,0.04)]",
        className
      )}
    >
      <div className="overflow-x-auto">
        <table
          className="w-full caption-bottom text-body"
          aria-label={ariaLabel}
          data-slot="data-table"
        >
          <thead className="border-b border-border bg-card">
            {table.getHeaderGroups().map((headerGroup) => (
              <tr key={headerGroup.id}>
                {selectable ? (
                  <th className="w-10 px-3 py-2 text-left align-middle">
                    <Checkbox
                      checked={table.getIsAllPageRowsSelected()}
                      onCheckedChange={(value) => table.toggleAllPageRowsSelected(!!value)}
                      aria-label="Select all rows"
                    />
                  </th>
                ) : null}
                {headerGroup.headers.map((header) => (
                  <th
                    key={header.id}
                    className="px-3 py-2 text-left align-middle text-label font-medium text-muted-foreground"
                    colSpan={header.colSpan}
                  >
                    {header.isPlaceholder ? null : (
                      <div
                        className={cn(
                          "inline-flex items-center gap-1",
                          header.column.getCanSort() && "cursor-pointer select-none"
                        )}
                        onClick={header.column.getToggleSortingHandler()}
                        aria-sort={
                          header.column.getIsSorted() === "asc"
                            ? "ascending"
                            : header.column.getIsSorted() === "desc"
                              ? "descending"
                              : undefined
                        }
                      >
                        {flexRender(header.column.columnDef.header, header.getContext())}
                        {header.column.getCanSort() ? (
                          header.column.getIsSorted() === "asc" ? (
                            <ArrowUpIcon className="size-3.5 text-foreground" aria-hidden="true" />
                          ) : header.column.getIsSorted() === "desc" ? (
                            <ArrowDownIcon className="size-3.5 text-foreground" aria-hidden="true" />
                          ) : (
                            <ChevronsUpDownIcon className="size-3.5 text-muted-foreground/50" aria-hidden="true" />
                          )
                        ) : null}
                      </div>
                    )}
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody className="divide-y divide-border">
            {loading
              ? Array.from({ length: skeletonRows }).map((_, index) => (
                  <tr key={index}>
                    {selectable ? (
                      <td className="px-3 py-2.5">
                        <Skeleton className="size-4 rounded-sm" />
                      </td>
                    ) : null}
                    {table.getVisibleLeafColumns().map((column) => (
                      <td key={column.id} className="px-3 py-2.5">
                        <Skeleton className="h-4 w-full max-w-40" />
                      </td>
                    ))}
                  </tr>
                ))
              : rows.map((row) => (
                  <tr key={row.id} className="hover:bg-muted">
                    {selectable ? (
                      <td className="px-3 py-2 align-middle">
                        <Checkbox
                          checked={row.getIsSelected()}
                          onCheckedChange={(value) => row.toggleSelected(!!value)}
                          aria-label="Select row"
                        />
                      </td>
                    ) : null}
                    {row.getVisibleCells().map((cell) => (
                      <td key={cell.id} className="px-3 py-2 align-middle">
                        {flexRender(cell.column.columnDef.cell, cell.getContext())}
                      </td>
                    ))}
                  </tr>
                ))}
          </tbody>
        </table>
      </div>

      {isEmpty ? (
        <div className="p-4">
          <EmptyState
            title={emptyTitle ?? (noResults ? "No results match this search" : "Nothing here yet")}
            description={emptyDescription}
            action={emptyAction}
          />
        </div>
      ) : null}

      {selectable && selectedRows.length > 0 ? (
        <div className="sticky bottom-0 flex flex-wrap items-center gap-3 border-t border-border bg-popover/95 px-4 py-2 backdrop-blur">
          <span className="text-label font-medium text-foreground">
            {selectedRows.length} selected
          </span>
          {bulkActions ? <div className="flex items-center gap-2">{bulkActions(selectedRows)}</div> : null}
          <Button variant="ghost" size="sm" onClick={() => table.resetRowSelection()}>
            Clear selection
          </Button>
        </div>
      ) : null}

      {!loading && !isEmpty ? (
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-4 py-2">
          <p className="text-caption text-muted-foreground tabular-nums">
            {pageStart}–{pageEnd} of {filteredData.length}
          </p>
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <span className="text-caption text-muted-foreground">Rows</span>
              <Select
                value={String(pageSize)}
                onValueChange={(value) => table.setPageSize(Number(value))}
              >
                <SelectTrigger size="sm" aria-label="Rows per page" className="w-20">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PAGE_SIZE_OPTIONS.map((size) => (
                    <SelectItem key={size} value={String(size)}>
                      {size}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() => table.previousPage()}
                disabled={!canPreviousPage}
                aria-label="Previous page"
              >
                <ChevronLeftIcon />
              </Button>
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() => table.nextPage()}
                disabled={!canNextPage}
                aria-label="Next page"
              >
                <ChevronRightIcon />
              </Button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}