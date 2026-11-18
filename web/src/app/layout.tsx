import type { Metadata } from "next";
import type { ReactNode } from "react";

import { FlagsProvider } from "@/lib/flags/client";
import { getFlagsOn } from "@/lib/flags/server";

import "./globals.css";

export const metadata: Metadata = {
  title: "Civic Scanner",
  description: "Track public agency meetings, agendas, and transcripts.",
};

export default async function RootLayout({
  children,
}: {
  children: ReactNode;
}) {
  return (
    <html lang="en">
      <body className="mx-auto max-w-3xl p-6 antialiased">
        <FlagsProvider on={await getFlagsOn()}>{children}</FlagsProvider>
      </body>
    </html>
  );
}
