import type { Metadata } from "next";
import type { ReactNode } from "react";

import { AccountMenu } from "@/components/account-menu";
import { Analytics } from "@/lib/analytics/analytics";
import { getAuthConfig } from "@/lib/auth/config";
import { FlagsProvider } from "@/lib/flags/client";
import { getFlagsOn } from "@/lib/flags/server";
import { readPostHogProject } from "@/lib/posthog/config";

import "./globals.css";

export const metadata: Metadata = {
  title: "Civic Scanner",
  description: "Track public agency meetings, agendas, and transcripts.",
};

const posthog = readPostHogProject();

export default async function RootLayout({
  children,
}: {
  children: ReactNode;
}) {
  const flagsOn = await getFlagsOn();
  const signIn = getAuthConfig().kind === "on" && flagsOn.includes("accounts");

  return (
    <html lang="en">
      <body className="mx-auto max-w-3xl p-6 antialiased">
        {signIn ? (
          <header className="mb-6 flex justify-end">
            <AccountMenu />
          </header>
        ) : null}
        <FlagsProvider on={flagsOn}>{children}</FlagsProvider>
        {posthog === undefined ? null : <Analytics project={posthog} />}
      </body>
    </html>
  );
}
