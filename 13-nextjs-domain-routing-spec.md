# Shopkeet — Next.js Multi-Domain Structure (admin / auth / storefront)

Covers how `apps/web` serves three distinct domains from one Next.js app, and the folder structure to build it in. Written against **Next.js 16** — if the project is ever downgraded to 15 or earlier, `proxy.ts`/`proxy` below reverts to `middleware.ts`/`middleware` (same logic, old file/export name).

## Domain model (stated assumptions — confirm if wrong)

| Domain | Serves | Tenant resolution |
|---|---|---|
| `auth.shopkeet.com` | Merchant signup, login, OTP, reset password | N/A — happens before a tenant session exists |
| `admin.shopkeet.com` | The merchant dashboard, unified across every tenant | From the logged-in merchant's JWT (`tenant_id` claim) — **not** the hostname |
| `{tenant}.shopkeet.com` | That tenant's storefront | From the subdomain, same as originally designed |
| Merchant custom domains (later) | That tenant's storefront | Needs an API lookup (`GET /tenants/by-domain`), not a subdomain parse — see note at the bottom |

**Customer-facing auth** (a shopper logging into their account on one store) is *not* on `auth.shopkeet.com` — it lives at `{tenant}.shopkeet.com/account/login`, in that store's own branding, using the separate customer JWT scope from `04-agent-build-spec.md` Phase 11. `auth.shopkeet.com` is merchant-only.

**One Next.js app, not Multi-Zones.** Three domains could be three separate Next.js deployments (Next's "Multi-Zones" feature exists for exactly this), but admin/auth/storefront share the entire component library from `11-frontend-foundation-build-spec.md` plus `lib/api.ts` — splitting now means either duplicating that or standing up a shared internal package for no current benefit. Revisit only if admin and storefront ever need genuinely independent deploy cadences or scaling — not before.

## Folder structure

Real top-level folders for the three domains, not route groups — a route group (`(name)`) is invisible to the resolved URL path, so it can't be used as a rewrite *destination* to disambiguate three domains landing on the same Next.js app. Route groups are still fine *inside* each of these for further organization (e.g. grouping admin screens under a shared layout without adding a path segment); they're just not the top-level split.

```
apps/web/
  proxy.ts                        # Next 16: was middleware.ts
  app/
    admin/
      layout.tsx                  # Sidebar + topbar shell — 12-admin-pages-build-spec.md Phase A
      page.tsx                    # Home/dashboard
      orders/
        page.tsx
        drafts/page.tsx
        abandoned/page.tsx
        [id]/page.tsx
      products/
        page.tsx
        collections/page.tsx
        inventory/page.tsx
        purchase-orders/page.tsx
        gift-cards/page.tsx
        [id]/page.tsx
      customers/
        page.tsx
        segments/page.tsx
        [id]/page.tsx
      growth/page.tsx
      discounts/
        page.tsx
        [id]/page.tsx
      content/
        pages/page.tsx
        templates/page.tsx
        blog-posts/page.tsx
        files/page.tsx
      analytics/page.tsx
      settings/page.tsx
    auth/
      layout.tsx                  # bare — centered card, no sidebar
      signup/page.tsx
      login/page.tsx
      otp/page.tsx
      reset-password/page.tsx
    storefront/
      [tenant]/
        layout.tsx                # reads tenant theme tokens; renders header/footer sections
        page.tsx                  # home (post_type='page', route='/')
        products/[slug]/page.tsx
        collections/[slug]/page.tsx
        cart/page.tsx
        checkout/page.tsx
        account/
          login/page.tsx          # CUSTOMER auth — separate from the merchant auth/ folder above
          orders/page.tsx
        [...slug]/page.tsx        # catch-all for other published posts
  components/
    admin/ auth/ storefront/ ui/  # ui/ = the Tier 1-5 library from 11-frontend-foundation-build-spec.md
  lib/
    api.ts
```

This directly implements the Phase A–H breakdown from `12-admin-pages-build-spec.md` as real routes — `app/admin/orders/drafts` is Phase B, `app/admin/products/purchase-orders` is Phase C, and so on. Build phase-by-phase against that doc; this file is just the folder shape it lands in.

## `proxy.ts`

```ts
// proxy.ts (Next.js 16 — was middleware.ts/middleware on 15 and earlier)
import { NextRequest, NextResponse } from 'next/server';

const ROOT_DOMAIN = process.env.NEXT_PUBLIC_ROOT_DOMAIN ?? 'shopkeet.com';

export function proxy(request: NextRequest) {
  const hostname = request.headers.get('host') ?? '';
  const { pathname } = request.nextUrl;

  if (hostname === `auth.${ROOT_DOMAIN}` || hostname.startsWith('auth.localhost')) {
    return NextResponse.rewrite(new URL(`/auth${pathname}`, request.url));
  }

  if (hostname === `admin.${ROOT_DOMAIN}` || hostname.startsWith('admin.localhost')) {
    return NextResponse.rewrite(new URL(`/admin${pathname}`, request.url));
  }

  if (hostname === ROOT_DOMAIN || hostname === `www.${ROOT_DOMAIN}`) {
    return NextResponse.next(); // marketing/landing page for shopkeet.com itself, if one exists
  }

  // Everything else: a tenant subdomain (custom domains need the lookup noted below)
  const tenantSlug = hostname.replace(`.${ROOT_DOMAIN}`, '');
  return NextResponse.rewrite(new URL(`/storefront/${tenantSlug}${pathname}`, request.url));
}

export const config = {
  matcher: ['/((?!_next|favicon.ico).*)'],
};
```

Local dev needs `auth.localhost:3000` / `admin.localhost:3000` / `{tenant}.localhost:3000` to resolve — modern browsers route `*.localhost` to `127.0.0.1` automatically with no `/etc/hosts` edit needed; confirm this works in whatever's actually used for local dev before relying on it.

## Session cookies across domains

The Go API's `/auth/login` keeps returning the JWT in the response body exactly as already built — no backend change. **Next.js itself** (a Route Handler wrapping that call, not the browser calling the Go API directly) sets the actual session cookie after receiving it:

```ts
cookies().set('shopkeet_session', jwt, {
  domain: `.${ROOT_DOMAIN}`,   // readable by auth., admin., and api. subdomains
  httpOnly: true,
  secure: true,
  sameSite: 'lax',
});
```

`Domain=.shopkeet.com` is what makes a cookie set during login on `auth.shopkeet.com` readable on `admin.shopkeet.com` afterward — this is the whole mechanism that makes "log in once, land on the unified admin" work. The **customer** session cookie (set from `{tenant}.shopkeet.com/account/login`) should **not** use this shared domain — scope it to that one tenant subdomain only, so one store's customer session can never be read on another tenant's storefront, even accidentally.

## Decided: Email OTP (SMS deferred)

**Decision (resolved):** the OTP gate is **email-based, delivered through Resend** (the same provider production transactional mail already uses; SMTP/Mailpit in dev). `auth/otp/page.tsx` is real: signup and login return `{mode:"otp_required", email, method:"email"}` until `/auth/otp/verify` accepts the code — see `docs/api-reference.md` §Auth and migration 0035.

**SMS OTP stays deferred** — it needs a new provider (Twilio or similar), another flagged non-open-source exception, and a real per-message cost at scale. That's a product/cost call to revisit deliberately, not an engineering default. When it happens, the backend shape is ready for it: `POST /auth/otp/send` already rejects non-email channels with `400 sms_unavailable`, so adding SMS is a new `method` branch plus a provider, not a schema change.

## Note: custom domains, for later

When merchant-connected custom domains exist (not `*.shopkeet.com`), `proxy.ts`'s subdomain-stripping logic above won't work — the whole hostname *is* the custom domain. That path needs `GET /tenants/by-domain?host=...` (cached — don't hit the database on every request for this) to resolve tenant from an arbitrary hostname. This is the same custom-domain question flagged as open in `03-architecture.md` §7 (Coolify per-domain attach vs. Caddy on-demand TLS) — resolve both together, since whatever answers "how does TLS get issued for this domain" also needs to answer "how does the Next.js app know which tenant it is."
