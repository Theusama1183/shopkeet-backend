import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";

import {
  STORE_PICK_COOKIE,
  authOriginFromHost,
  decodeStorePickTicket,
  getSession,
} from "@/lib/session";

/**
 * The auth gate for every admin page that needs a live merchant session.
 *
 * It sits in a nested layout rather than the /admin shell because two pages must
 * be reachable without one: the store list (a merchant who logged into an
 * account with several stores has no session yet — only the store_pick ticket)
 * and the setup wizard (whose own page redirects away when the session is
 * already onboarded, so the two can never bounce off each other).
 */
export default async function GuardedLayout({ children }: { children: React.ReactNode }) {
  const host = (await headers()).get("host") ?? "";
  const session = await getSession();

  if (!session) {
    const jar = await cookies();
    const ticket = jar.get(STORE_PICK_COOKIE)?.value;
    if (ticket && decodeStorePickTicket(ticket)) {
      // Mid-pick: the store list is the only page this browser may open, and it
      // lives outside this layout so this redirect can never point at itself.
      redirect("/admin/stores");
    }
    redirect(`${authOriginFromHost(host)}/login`);
  }

  // A session minted by signup names a store nobody has set up yet. The wizard
  // runs once; until it does, no other admin page renders for that store.
  if (session.claims.onboarding_completed === false) {
    redirect("/admin/onboarding");
  }

  return <>{children}</>;
}
