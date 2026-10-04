"use client"

import { useState } from "react"
import { toast } from "sonner"
import {
  BellIcon,
  CheckIcon,
  InfoIcon,
  PlusIcon,
} from "lucide-react"

import { Section } from "@/components/dev/section"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { Separator } from "@/components/ui/separator"
import { Card } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Spinner } from "@/components/feedback/spinner"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/feedback/empty-state"
import { ErrorState } from "@/components/feedback/error-state"
import { ConfirmDialog } from "@/components/feedback/confirm-dialog"
import { StatusBadge } from "@/components/admin/status-badge"

const TOKEN_NAMES = [
  ["background", "var(--background)"],
  ["foreground", "var(--foreground)"],
  ["card", "var(--card)"],
  ["popover", "var(--popover)"],
  ["primary", "var(--primary)"],
  ["secondary", "var(--secondary)"],
  ["muted", "var(--muted)"],
  ["accent", "var(--accent)"],
  ["destructive", "var(--destructive)"],
  ["warning", "var(--warning)"],
  ["success", "var(--success)"],
  ["border", "var(--border)"],
] as const

const TYPE_SCALE = [
  ["text-caption", "The quick brown fox"],
  ["text-label", "The quick brown fox"],
  ["text-body", "The quick brown fox"],
  ["text-lead", "The quick brown fox"],
  ["text-h3", "The quick brown fox"],
  ["text-h2", "The quick brown fox"],
  ["text-h1", "The quick brown fox"],
  ["text-display", "The quick brown fox"],
] as const

export function PrimitivesDemo() {
  const [dialogOpen, setDialogOpen] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [flavor, setFlavor] = useState("vanilla")
  const [notify, setNotify] = useState(true)

  return (
    <div className="flex flex-col gap-12">
      <Section
        id="tokens"
        title="Design tokens"
        description="Both token sets are CSS variables, not Tailwind's stock palette. Swatch below shows the admin set; the storefront set re-themes per tenant (see the storefront section)."
      >
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-6">
          {TOKEN_NAMES.map(([name, value]) => (
            <div
              key={name}
              className="flex items-center gap-2 rounded-lg border border-border bg-card p-2"
            >
              <span
                className="size-6 shrink-0 rounded-md border border-border"
                style={{ backgroundColor: value }}
                aria-hidden="true"
              />
              <span className="truncate text-caption text-muted-foreground">{name}</span>
            </div>
          ))}
        </div>
        <div className="flex flex-col gap-1 rounded-lg border border-border bg-card p-4">
          {TYPE_SCALE.map(([className, sample]) => (
            <div key={className} className="flex items-baseline gap-3">
              <span className="w-24 shrink-0 text-caption text-muted-foreground">{className}</span>
              <span className={className}>{sample}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section id="buttons" title="Buttons" description="Shadcn-sourced, re-themed through tokens. Variants, sizes, disabled, and the reserved small-inline spinner.">
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-4">
          <Button>Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="outline">Outline</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="destructive">Delete</Button>
          <Button variant="link">Link</Button>
          <Button size="sm">Small</Button>
          <Button size="lg">Large</Button>
          <Button disabled>Disabled</Button>
          <Button>
            <Spinner label="Saving" />
          </Button>
          <Button variant="destructive" size="sm">
            <PlusIcon /> Add
          </Button>
          <Button variant="outline" size="icon">
            <BellIcon className="size-4" />
            <span className="sr-only">Notifications</span>
          </Button>
        </div>
      </Section>

      <Section id="forms" title="Form controls" description="Styled on the same tokens so a form never invents its own input.">
        <div className="grid gap-4 rounded-lg border border-border bg-card p-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="demo-name">Product name</Label>
            <Input id="demo-name" placeholder="e.g. Stoneware mug" defaultValue="Stoneware mug" />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="demo-email">Email</Label>
            <Input id="demo-email" type="email" placeholder="you@store.com" aria-invalid="true" />
            <span className="text-caption text-destructive">Enter a valid email address.</span>
          </div>
          <div className="flex flex-col gap-1.5 sm:col-span-2">
            <Label htmlFor="demo-desc">Description</Label>
            <Textarea id="demo-desc" rows={3} placeholder="Short description shown on the product page." />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label>Status</Label>
            <Select value="active">
              <SelectTrigger className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="active">Active</SelectItem>
                <SelectItem value="draft">Draft</SelectItem>
                <SelectItem value="archived">Archived</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <RadioGroup defaultValue="collect" className="flex flex-col gap-1.5">
            <Label>Local pickup</Label>
            <div className="flex items-center gap-2 text-body">
              <RadioGroupItem value="collect" id="pickup-collect" />
              <Label htmlFor="pickup-collect">Collect at store</Label>
            </div>
            <div className="flex items-center gap-2 text-body">
              <RadioGroupItem value="wrapped" id="pickup-wrapped" />
              <Label htmlFor="pickup-wrapped">Wrap and ship</Label>
            </div>
          </RadioGroup>
          <div className="flex items-center justify-between gap-2 sm:col-span-2">
            <div className="flex items-center gap-2">
              <Checkbox id="demo-featured" />
              <Label htmlFor="demo-featured">Feature on the storefront</Label>
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor="demo-notify" className="text-muted-foreground">
                Low-stock notifications
              </Label>
              <Switch id="demo-notify" checked={notify} onCheckedChange={setNotify} />
            </div>
          </div>
        </div>
      </Section>

      <Section id="badges" title="Badges, status, avatar, tooltip" description="StatusBadge owns the one status→color mapping used everywhere.">
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-4">
          <Badge>Default</Badge>
          <Badge variant="secondary">Secondary</Badge>
          <Badge variant="outline">Outline</Badge>
          <StatusBadge tone="neutral">Unfulfilled</StatusBadge>
          <StatusBadge tone="success">Paid</StatusBadge>
          <StatusBadge tone="warning">Low stock</StatusBadge>
          <StatusBadge tone="danger">Cancelled</StatusBadge>
          <StatusBadge tone="info">Pending</StatusBadge>
          <Separator orientation="vertical" className="h-6" />
          <Avatar>
            <AvatarFallback>MK</AvatarFallback>
          </Avatar>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="outline" size="sm">
                Hover me
              </Button>
            </TooltipTrigger>
            <TooltipContent side="top">
              <p>Tooltips stay short.</p>
            </TooltipContent>
          </Tooltip>
        </div>
      </Section>

      <Section id="feedback" title="Feedback & state" description="Loading defaults to skeletons for primary content; the spinner is only for inline affordances. EmptyState says what to do next; ErrorState always offers a retry.">
        <div className="grid gap-4 rounded-lg border border-border bg-card p-4 lg:grid-cols-2">
          <div className="flex flex-col gap-3">
            <Spinner label="Syncing orders" />
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <Skeleton className="size-9 rounded-md" />
                <div className="flex flex-1 flex-col gap-1">
                  <Skeleton className="h-3 w-3/4" />
                  <Skeleton className="h-3 w-1/2" />
                </div>
              </div>
              <Skeleton className="h-2.5 w-full" />
              <Skeleton className="h-2.5 w-5/6" />
            </div>
            <Alert>
              <InfoIcon className="size-4" />
              <AlertTitle>Inline notice</AlertTitle>
              <AlertDescription>Something changed that the merchant should know.</AlertDescription>
            </Alert>
            <Alert variant="destructive">
              <InfoIcon className="size-4" />
              <AlertTitle>Catalog error</AlertTitle>
              <AlertDescription>This slug is already used by another product.</AlertDescription>
            </Alert>
          </div>
          <div className="flex flex-col gap-3">
            <EmptyState
              icon={CheckIcon}
              title="Add your first product"
              description="Products appear here once you publish your inventory."
              action={<Button size="sm">Add a product</Button>}
            />
            <ErrorState
              title="Orders couldn't load"
              description="The orders feed is unreachable right now."
              onRetry={() => toast.success("Refetched — all good.")}
            />
          </div>
        </div>
        <div className="flex flex-wrap gap-2 rounded-lg border border-border bg-card p-4">
          <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
            <DialogTrigger asChild>
              <Button variant="outline">Open dialog</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Add product</DialogTitle>
                <DialogDescription>
                  A dialog for a detail form, not for every little thing.
                </DialogDescription>
              </DialogHeader>
              <p className="text-body text-muted-foreground">
                Real forms land with the catalog screens.
              </p>
              <DialogFooter>
                <Button variant="ghost" onClick={() => setDialogOpen(false)}>
                  Close
                </Button>
                <Button onClick={() => setDialogOpen(false)}>Save changes</Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          <Sheet>
            <SheetTrigger asChild>
              <Button variant="outline">Open sheet</Button>
            </SheetTrigger>
            <SheetContent side="right">
              <SheetHeader>
                <SheetTitle>Filters</SheetTitle>
                <SheetDescription>Side panel for heavier filtering or quick edits.</SheetDescription>
              </SheetHeader>
            </SheetContent>
          </Sheet>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline">Menu</Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuLabel>Order #1012</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => toast.info("Marked paid")}>Mark paid</DropdownMenuItem>
              <DropdownMenuItem onClick={() => toast.info("Invoice emailed")}>Email invoice</DropdownMenuItem>
              <DropdownMenuItem>Print shipping label</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <ConfirmDialog
            open={confirmOpen}
            onOpenChange={setConfirmOpen}
            title="Cancel order #1012?"
            description="This can't be undone. The customer's payment is already captured and will need a manual refund."
            confirmLabel="Cancel order"
            onConfirm={() => {
              setConfirmOpen(false)
              toast.success("Order #1012 cancelled.")
            }}
          />
          <Button variant="destructive" size="sm" onClick={() => setConfirmOpen(true)}>
            Trigger ConfirmDialog
          </Button>
        </div>
      </Section>

      <Section id="layout" title="Tabs & cards" description="Cards are for summary moments, not every piece of content (admin rule).">
        <Tabs defaultValue="overview">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="catalog">Catalog</TabsTrigger>
            <TabsTrigger value="orders">Orders</TabsTrigger>
          </TabsList>
          <TabsContent value="overview">
            <Card className="p-4">The overview tab body.</Card>
          </TabsContent>
          <TabsContent value="catalog">
            <Card className="p-4">The catalog tab body.</Card>
          </TabsContent>
          <TabsContent value="orders">
            <Card className="p-4">{flavor} orders, maybe — a tab body can hold a table.</Card>
            <Button size="sm" variant="outline" onClick={() => setFlavor("latte")}>
              Rename (interaction check)
            </Button>
          </TabsContent>
        </Tabs>
      </Section>
    </div>
  )
}