/**
 * Inline SVG data-URI thumbnails for the /dev/components gallery so it never
 * depends on external image hosts. Distinct per `hue` so cards read as
 * different products.
 */
export function sampleImage(hue: number): string {
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="800">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
    `<stop offset="0" stop-color="hsl(${hue} 42% 84%)"/>` +
    `<stop offset="1" stop-color="hsl(${hue} 55% 66%)"/>` +
    `</linearGradient></defs>` +
    `<rect width="640" height="800" fill="url(#g)"/>` +
    `</svg>`
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`
}