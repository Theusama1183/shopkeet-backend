import type { Metadata } from "next";
import { Geist, Geist_Mono, Inter } from "next/font/google";

import { TooltipProvider } from "@/components/ui/tooltip";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

// Shopify admin renders in Inter; only the /admin shell opts in (via
// .admin-surface), so storefront and auth keep Geist.
const inter = Inter({
  variable: "--font-inter",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: {
    default: "Shopkeet",
    template: "%s — Shopkeet",
  },
  description:
    "Shopkeet — the e-commerce platform for solo and small independent merchants.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} ${inter.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col">
        {/* One provider for every Tooltip in the app — shadcn's sidebar menu
            buttons and the sales chart both render <Tooltip> directly, and
            without an ancestor provider they throw at render. */}
        <TooltipProvider>{children}</TooltipProvider>
      </body>
    </html>
  );
}
