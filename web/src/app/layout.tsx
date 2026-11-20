import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";

import { AccountMenu } from "@/components/account-menu";
import { SiteNav } from "@/components/site-nav";
import { Analytics } from "@/lib/analytics/analytics";
import { hasPublicRedirectUri } from "@/lib/auth/config";
import { FlagsProvider } from "@/lib/flags/client";
import { getFlagsOn } from "@/lib/flags/server";
import { readPostHogProject } from "@/lib/posthog/config";

import "./globals.css";

const SITE_NAME = "Civic Scanner";

export const metadata: Metadata = {
  title: { default: SITE_NAME, template: `%s | ${SITE_NAME}` },
  description:
    "Follow the public meetings and agendas of Southern California transportation agencies.",
};

const posthog = readPostHogProject();

const CONTAINER = "mx-auto w-full max-w-4xl px-4 sm:px-6";

export default async function RootLayout({
  children,
}: {
  children: ReactNode;
}) {
  const flagsOn = await getFlagsOn();
  const signIn = hasPublicRedirectUri() && flagsOn.includes("accounts");

  return (
    <html lang="en">
      <body className="flex min-h-dvh flex-col bg-canvas font-sans text-ink antialiased">
        <a
          href="#main"
          className="sr-only rounded-md bg-surface px-4 py-2 font-medium text-accent shadow focus:not-sr-only focus:absolute focus:top-3 focus:left-3 focus:z-50"
        >
          Skip to main content
        </a>
        <FlagsProvider on={flagsOn}>
          <header className="border-b border-line bg-surface">
            <div
              className={`${CONTAINER} flex flex-wrap items-center gap-x-8 gap-y-2 py-4`}
            >
              <Link
                href="/"
                className="text-lg font-semibold tracking-tight text-ink"
              >
                {SITE_NAME}
              </Link>
              <SiteNav />
              {signIn ? (
                <div className="ml-auto flex h-6 items-center text-sm">
                  <AccountMenu />
                </div>
              ) : null}
            </div>
          </header>
          <main
            id="main"
            tabIndex={-1}
            className={`${CONTAINER} flex-1 py-10 focus:outline-none sm:py-14`}
          >
            {children}
          </main>
          <footer className="border-t border-line bg-surface">
            <div
              className={`${CONTAINER} space-y-2 py-8 text-sm text-ink-muted`}
            >
              <p>
                {SITE_NAME} follows the public meetings of Southern California
                transportation agencies and their agendas.
              </p>
              <p>
                Summaries are written by AI and can be wrong. The agency&apos;s
                own documents and recordings are the official record.
              </p>
            </div>
          </footer>
        </FlagsProvider>
        {posthog === undefined ? null : <Analytics project={posthog} />}
      </body>
    </html>
  );
}
