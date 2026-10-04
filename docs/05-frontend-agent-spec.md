# Shopkeet — Frontend Agent Spec

This spec governs everything built in `apps/web`. Read it alongside `04-agent-build-spec.md`, which defines the API this frontend talks to — this document defines how it should *look and feel*, not just what it does.

## The core directive

Shopkeet's frontend must read as the work of an experienced product design engineer who made deliberate choices — not as output from an AI page-builder. Merchants using the admin, and their customers browsing storefronts, should never have the thought "this looks AI-generated." That thought is triggered by specific, nameable patterns, not a vague vibe — so avoid the specific patterns below, not just "try to make it good."

There are two distinct surfaces here, and they should not look like the same product wearing two skins:

- **Merchant admin** — a serious business tool. The bar is Stripe Dashboard, Linear, Vercel's dashboard: dense but organized, fast, clear hierarchy, decoration earns its place or it's cut.
- **Storefronts** — customer-facing, one per merchant, and they must not all look like the same template with a different logo slapped on. A merchant selling handmade pottery and a merchant selling phone accessories should not produce visually interchangeable stores. This is what the block-based page builder (`03-architecture.md` §6) needs to actually support — real variation in color, type, and layout per tenant, not just swapped copy.

## Specifically avoid these — the tells of AI-generated UI

These are not style opinions; they are the concrete, recognizable patterns that make a page read as machine-generated rather than designed. Do not use them as defaults on either surface:

- A warm cream background with a high-contrast serif headline and a terracotta/clay accent color (~`#D97757`) — this specific combination is the single most recognizable "AI-generated" tell right now.
- A near-black background with one bright acid-green or vermilion accent color.
- The "SaaS card kit": everything chopped into identical rounded cards, one uniform border-radius applied regardless of hierarchy, the same soft grey drop-shadow under every card, gradient washes used purely as decoration.
- Template chrome that shows up regardless of subject matter: tracked-out ALL-CAPS eyebrow labels above every heading; meta text joined with middle dots (`A · B · C`); labels styled as `WORD — fragment` with a spaced em dash; a monospace font for small data labels for no functional reason; a `→` tacked onto every link and button.
- Centered hero + three feature cards + testimonials, as the default page shape, applied without asking whether it's actually right for this content.
- Scattered motion: fade-and-slide-up on every section as it scrolls into view, a hover transition on every single card. Motion should be rare and purposeful — one orchestrated moment, or a response to something the user just did (opened a menu, confirmed an action) — not ambient decoration on everything.
- Numbered markers (`01 / 02 / 03`) used as a design device on content that isn't actually a sequence.
- Bolding or italicizing a single word in a headline for "emphasis" with no real reason.

None of these are permanently forbidden — they're defaults, and defaults applied without a reason are what read as generated. If one is genuinely the right choice for a specific screen, that's a decision to make deliberately and be able to explain, not a fallback.

## Design process (do this before writing code for any new screen or storefront theme)

1. **Name the one sentence:** what is this screen's job, and for whom? ("This is where a merchant sees today's orders at a glance" is different from "this is where a customer decides to buy.")
2. **Set a token plan before building:** 4–6 named colors, one or two typefaces with clear roles (don't default to whatever font ships with the starter template), a layout concept in a sentence plus a rough wireframe.
3. **Check the plan against the tells list above.** If it matches one without a specific reason tied to this screen, revise it.
4. **Build to a quality floor, quietly:** responsive down to mobile (a large share of storefront customers will be on phones), visible keyboard focus states, reduced-motion respected, real color contrast — do this without announcing it in the UI.
5. **Cut one thing before shipping.** If in doubt whether a decorative element earns its place, it probably doesn't.

## Admin dashboard specifics

- Information density over whitespace-for-its-own-sake — merchants are checking orders and inventory repeatedly, not being sold to.
- Tables and lists are the primary UI, not cards, for anything list-like (orders, products). Cards are for a handful of summary numbers, not every piece of content.
- Every destructive or payment-affecting action (cancel order, mark delivered, delete product) needs a clear confirmation and an undo path or an explicit warning — this is a business's real orders and money.
- Loading and empty states are informative, not decorative: an empty products list says what to do next ("Add your first product"), not just "No data."

## Storefront specifics

- Each tenant's storefront must be able to express a distinct identity: configurable brand color(s), a couple of font pairing options (not infinite choice, but not one fixed look either), and layout flexibility through the block registry (`HeroBanner`, `ProductGrid`, more later).
- Product-focused pages should default to letting the product images and names carry the design — not compete with heavy decorative chrome around them.
- Checkout must be the calmest, clearest screen in the whole app: minimal navigation, nothing competing for attention with "complete this purchase."

## Copy and microcopy

- Write from the customer's or merchant's perspective, in plain language — "Orders," not "Order management module"; "Add to cart," not "Add item to shopping cart instance."
- Buttons say exactly what happens: "Save changes," "Mark as delivered" — not "Submit." Keep the same word through a flow: a "Publish" button leads to a "Published" confirmation, not "Success!"
- Avoid generic SaaS-copy clichés — no "Supercharge your store," "Unlock the power of...," "Your all-in-one solution." Say the specific thing plainly.
- Error messages state what went wrong and what to do, without apologizing or being vague ("This slug is already used by another product" beats "Something went wrong").

## Technical conventions

- Design tokens (colors, spacing scale, type scale) live as CSS variables / a Tailwind theme config, not scattered magic numbers in component files. Storefront per-tenant theming reads from tenant config into these same tokens.
- Component structure mirrors backend module boundaries where it makes sense: `components/catalog/`, `components/cart/`, `components/admin/`, `components/content/` (the Puck block registry — `HeroBanner`, `ProductGrid`, and others), rather than one flat `components/` dump.
- The admin's page/template/section editors are built on **Puck** (`@puckeditor/core`) — do not hand-build a drag-and-drop canvas. Blocks in `components/content/` double as Puck's `config.components`; the same components render both the live editor preview and the published storefront via Puck's `<Render>`.
- Server components by default (per `04-agent-build-spec.md` conventions); reach for client components only where real interactivity requires it (cart state, form handling, the Puck editor itself), not by default.
- Images: use Next.js `<Image>` with real width/height, not unoptimized `<img>` tags, pointed at the Cloudflare R2 public URL (configured as a remote image domain) — storefronts are judged partly on how fast product pages load.
- Accessibility is a floor, not a stretch goal: semantic HTML elements over generic `<div>`s with click handlers, real focus states, sufficient contrast — check this before calling a screen done, not after.
