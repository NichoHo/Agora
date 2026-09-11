import type { Metadata } from "next";
import { Plus_Jakarta_Sans } from "next/font/google";
import Link from "next/link";
import BackLink from "@/components/BackLink";
import StickyHeader from "@/components/motion/StickyHeader";
import { Input } from "@/components/ui/input";
import { buttonVariants } from "@/components/ui/button";
import { getUser } from "@/lib/auth";
import { ADMIN_EMAILS } from "@/lib/env";
import "./globals.css";

const plusJakarta = Plus_Jakarta_Sans({ variable: "--font-plus-jakarta", subsets: ["latin"] });

export const metadata: Metadata = {
  title: "Vault",
  description: "C2C marketplace with self-built identity and escrow",
};

async function Header() {
  const user = await getUser();
  const navLink = "text-muted-foreground transition-colors hover:text-ink";
  return (
    <header className="border-b border-line glass">
      <div className="mx-auto flex max-w-6xl items-center gap-4 px-4 py-3 sm:px-6 lg:px-8">
        <Link href="/" className="shrink-0 text-lg font-bold tracking-tight text-ink">
          Vault<span className="text-primary">.</span>
        </Link>
        <form action="/search" role="search" className="flex-1">
          <Input
            type="search"
            name="q"
            aria-label="Search listings"
            autoComplete="off"
            placeholder="Search listings…"
            className="max-w-xl border-line bg-surface/80 focus-visible:bg-surface"
          />
        </form>
        <nav className="flex items-center gap-4 text-sm">
          <Link href="/sell" className={buttonVariants({ className: "font-semibold" })}>
            Sell
          </Link>
          {user ? (
            <>
              <Link href="/orders" className={`hidden sm:inline ${navLink}`}>
                Orders
              </Link>
              <Link href="/wallet" className={`hidden sm:inline ${navLink}`}>
                Wallet
              </Link>
              {ADMIN_EMAILS.includes(user.email.toLowerCase()) ? (
                <Link
                  href="/admin"
                  className="hidden text-warning transition-colors hover:text-ink sm:inline"
                >
                  Admin
                </Link>
              ) : null}
              <Link
                href="/auth/mfa"
                title="Two-factor authentication"
                className="text-primary transition-colors hover:text-ink"
              >
                2FA
              </Link>
              <a href="/auth/logout" className={navLink}>
                Sign out
              </a>
            </>
          ) : (
            <a
              href="/auth/start"
              className="font-medium text-primary transition-colors hover:text-ink"
            >
              Sign in
            </a>
          )}
        </nav>
      </div>
    </header>
  );
}

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`${plusJakarta.variable} h-full antialiased`}>
      <body className="flex min-h-full flex-col bg-canvas font-sans">
        <a
          href="#main"
          className="sr-only focus-visible:not-sr-only focus-visible:absolute focus-visible:left-4 focus-visible:top-3 focus-visible:z-50 focus-visible:rounded-control focus-visible:bg-surface focus-visible:px-3 focus-visible:py-1.5 focus-visible:text-sm focus-visible:font-medium focus-visible:text-ink focus-visible:shadow-md"
        >
          Skip to content
        </a>
        <StickyHeader>
          <Header />
        </StickyHeader>
        <main
          id="main"
          className="mx-auto w-full max-w-6xl flex-1 px-4 py-8 sm:px-6 sm:py-12 lg:px-8"
        >
          <BackLink />
          {children}
        </main>
        <footer className="border-t border-line py-6 text-center text-xs text-faint">
          Vault is a portfolio project. No real money, simulated everything.
        </footer>
      </body>
    </html>
  );
}
