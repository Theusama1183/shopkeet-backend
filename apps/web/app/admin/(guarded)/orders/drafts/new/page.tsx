import Link from "next/link";
import { ShoppingBagIcon } from "lucide-react";

import { adminRequest } from "@/lib/admin-api";
import type { AdminProductsResponse, ShippingRatesResponse } from "@/lib/admin-types";
import { PageHeader } from "@/components/admin/page-header";
import { NewDraftForm } from "@/components/admin/new-draft-form";
import { Button } from "@/components/ui/button";

export default async function NewDraftPage() {
  const [productsData, ratesData] = await Promise.all([
    adminRequest<AdminProductsResponse>("/products"),
    adminRequest<ShippingRatesResponse>("/shipping/rates"),
  ]);

  const sellable = productsData.products.filter(
    (product) => product.status === "active" && product.variants.some((v) => v.inventory_count > 0)
  );

  if (sellable.length === 0) {
    return (
      <>
        <PageHeader
          crumbs={[{ label: "Orders", href: "/admin/orders" }, { label: "New draft" }]}
          title="New draft"
          icon={ShoppingBagIcon}
          description="A phone or in-person sale, created by you."
        />
        <Button asChild>
          <Link href="/admin/products">Add products first</Link>
        </Button>
      </>
    );
  }

  return (
    <>
      <PageHeader
        crumbs={[{ label: "Orders", href: "/admin/orders" }, { label: "New draft" }]}
        title="New draft"
        icon={ShoppingBagIcon}
        description="A phone or in-person sale, created by you."
      />
      <NewDraftForm products={sellable} rates={ratesData.rates} stateRequired={ratesData.state_required} />
    </>
  );
}