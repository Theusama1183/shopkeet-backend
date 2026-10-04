"use client"

import { CheckIcon, ImagePlusIcon, ImagesIcon } from "lucide-react"

import { cn } from "cn"
import { Button } from "@/components/ui/button"
import { EmptyState } from "@/components/feedback/empty-state"

export interface MediaItem {
  id: string
  url: string
  alt?: string
}

export interface MediaPickerProps {
  value?: string | null
  onChange: (id: string) => void
  media: MediaItem[]
  /**
   * Opens upload against the R2 presigned flow (media endpoints ship in the
   * Phase 2 frontend pass). Foundation renders the affordance only.
   */
  onUpload?: () => void
  className?: string
}

/**
 * Browses/uploads against the R2 media endpoints. Data plumbing lands with the
 * media-phase screen; the picker UI and selection state are foundation.
 */
export function MediaPicker({
  value,
  onChange,
  media,
  onUpload,
  className,
}: MediaPickerProps) {
  return (
    <div className={cn("flex flex-col gap-3", className)}>
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-4">
        {media.map((item) => {
          const selected = value === item.id
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => onChange(item.id)}
              aria-pressed={selected}
              aria-label={`Select media ${item.alt ?? item.id}`}
              className={cn(
                "group relative aspect-square overflow-hidden rounded-md border bg-muted focus-visible:outline-2 focus-visible:outline-ring",
                selected ? "border-primary ring-2 ring-ring" : "border-border"
              )}
            >
              {/* eslint-disable-next-line @next/next/no-img-element -- remote R2 URLs, sized by the grid */}
              <img
                src={item.url}
                alt={item.alt ?? ""}
                className="size-full object-cover"
              />
              {selected ? (
                <span className="absolute top-1.5 right-1.5 flex size-5 items-center justify-center rounded-full bg-primary text-primary-foreground">
                  <CheckIcon className="size-3" aria-hidden="true" />
                </span>
              ) : null}
            </button>
          )
        })}
        {onUpload ? (
          <button
            type="button"
            onClick={onUpload}
            className="flex aspect-square flex-col items-center justify-center gap-1 rounded-md border border-dashed border-border text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground"
          >
            <ImagePlusIcon className="size-5" aria-hidden="true" />
            <span className="text-caption">Upload</span>
          </button>
        ) : null}
      </div>
      {media.length === 0 ? (
        <EmptyState
          icon={ImagesIcon}
          title="No media yet"
          description="Upload a product photo to get started."
          action={
            onUpload ? (
              <Button variant="outline" onClick={onUpload}>
                Upload media
              </Button>
            ) : undefined
          }
        />
      ) : null}
    </div>
  )
}