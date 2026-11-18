import type { Metadata } from "next";
import type { ReactNode } from "react";

import { FlagsProvider } from "@/lib/flags/client";

import "./globals.css";

export const metadata: Metadata = {
  title: "Civic Scanner",
  description: "Track public agency meetings, agendas, and transcripts.",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body className="mx-auto max-w-3xl p-6 antialiased">
        <FlagsProvider>{children}</FlagsProvider>
      </body>
    </html>
  );
}
