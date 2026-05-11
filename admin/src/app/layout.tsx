import type { Metadata } from "next";

import "./codemirror-override.css";
import "./globals.css";

import { DialogProvider } from "@/components/create-dialog";
import { ThemeProvider } from "next-themes";

const SITE_TITLE = process.env.ADMIN_TITLE ?? "MesaHub - Admin";

export const metadata: Metadata = {
  title: SITE_TITLE,
  description: "Centralized SQLite storage service for internal Railway workloads",
};

export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body suppressHydrationWarning>
        <ThemeProvider
          attribute="class"
          defaultTheme="dark"
          forcedTheme="dark"
          disableTransitionOnChange
        >
          {children}
          <DialogProvider slot="default" />
        </ThemeProvider>
      </body>
    </html>
  );
}
