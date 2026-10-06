/**
 * Response shapes for the endpoints the Phase B admin pages consume. Mirrors
 * docs/api-reference.md and the Go handlers exactly (orderJSON in orders.go,
 * abandoned.go, analytics.go).
 */

export interface AdminOrderItem {
  id: string;
  product_id: string;
  variant_id: string;
  quantity: number;
  unit_price_cents: number;
  line_total_cents: number;
  bundle_id: string | null;
}

export interface AdminOrder {
  id: string;
  customer_id: string | null;
  customer_name: string;
  customer_phone: string;
  customer_email: string | null;
  shipping_address: string | null;
  shipping_address_line1: string | null;
  shipping_address_line2: string | null;
  shipping_city: string | null;
  shipping_state: string | null;
  shipping_postal_code: string | null;
  shipping_country: string | null;
  shipping_method: string | null;
  shipping_cost_cents: number;
  discount_code: string | null;
  discount_cents: number;
  gift_card_code: string | null;
  gift_card_cents: number;
  tax_cents: number;
  internal_note: string | null;
  payment_method: string;
  payment_status: string;
  status: string;
  source: string;
  total_cents: number;
  currency: string;
  tracking_number: string | null;
  tracking_carrier: string | null;
  tracking_url: string | null;
  created_at: string;
  items: AdminOrderItem[];
}

export interface AdminOrdersResponse {
  orders: AdminOrder[];
}

export interface AbandonedCart {
  id: string;
  customer_email: string;
  created_at: string;
  last_activity_at: string;
  recovery_sent_at: string | null;
  item_count: number;
  total_cents: number;
}

export interface AbandonedCartsResponse {
  carts: AbandonedCart[];
}

export interface SalesBucket {
  date: string;
  revenue_cents: number;
  order_count: number;
}

export interface SalesAnalytics {
  period: string;
  currency: string;
  totals: { revenue_cents: number; order_count: number };
  buckets: SalesBucket[];
}

export interface TopProductItem {
  product_id: string;
  product_name: string;
  quantity: number;
  revenue_cents: number;
}

export interface TopProductsAnalytics {
  period: string;
  metric: string;
  currency: string;
  items: TopProductItem[];
}

export interface ConversionAnalytics {
  period: string;
  carts_created: number;
  orders_placed: number;
  conversion_rate: number;
}

export interface ShippingRate {
  id: string;
  zone_id: string;
  zone_name: string;
  name: string;
  rate_cents: number;
  free_over_cents: number | null;
  sort_order: number;
}

export interface ShippingRatesResponse {
  rates: ShippingRate[];
  state_required: boolean;
}

export interface ProductVariantOptionValue {
  option_value_id: string;
  option_id: string;
  option_name: string;
  value: string;
}

export interface ProductVariant {
  id: string;
  sku: string | null;
  price_cents: number;
  inventory_count: number;
  weight_grams: number | null;
  status: string;
  allow_preorder: boolean;
  preorder_ships_at: string | null;
  option_values: ProductVariantOptionValue[];
}

export interface AdminProduct {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  price_cents: number;
  currency: string;
  inventory_count: number;
  status: string;
  rating_average: number | null;
  rating_count: number;
  created_at: string;
  variants: ProductVariant[];
}

export interface AdminProductsResponse {
  products: AdminProduct[];
}

/** Grouped record search backing the admin command palette (/search). */
export interface SearchResponse {
  products: Array<{ id: string; name: string; status: string }>;
  orders: Array<{
    id: string;
    customer_name: string;
    status: string;
    total_cents: number;
    currency: string;
  }>;
  customers: Array<{ id: string; email: string; phone: string }>;
}

export const ORDER_SOURCES = ["storefront", "draft"] as const;
// --- store profile (the one-time onboarding wizard + settings) ---------------

export interface TenantSettings {
  id: string;
  name: string;
  subdomain: string;
  logo_media_asset_id: string;
  default_currency: string;
  timezone: string;
  support_email: string;
  support_phone: string;
  tax_rate_percent: number;
  loyalty_points_per_currency_unit: number;
  loyalty_redemption_rate: number;
  onboarding_completed: boolean;
}

export interface TenantSettingsResponse {
  settings: TenantSettings;
}

/** A store the merchant's account can open (login, /auth/stores). */
export interface MerchantStore {
  tenant_id: string;
  name: string;
  subdomain: string;
  role: string;
  status: string;
  onboarding_completed: boolean;
}
