import type { Metadata } from "next";

import "./codemirror-override.css";
import "./globals.css";

import { DialogProvider } from "@/components/create-dialog";
import { ThemeProvider } from "next-themes";

export const metadata: Metadata = {
  title: "mesahub",
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
