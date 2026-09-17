import type { Metadata, Viewport } from "next";
import { Inter, Merriweather } from "next/font/google";
import { ClientProviders } from "@/components/client-providers";
import { PaletteBootScript } from "@/components/palette-boot-script";
import { ServerProviders } from "@/components/server-providers";
import { resolveDefaultPalette } from "@/lib/palettes";
import "./globals.css";

const inter = Inter({
  variable: "--font-inter",
  subsets: ["latin"],
});

const merriweather = Merriweather({
  variable: "--font-merriweather",
  subsets: ["latin"],
  weight: ["300", "400", "700"],
});

export const metadata: Metadata = {
  title: {
    template: "%s — Mecatl Studio",
    default: "Mecatl Studio",
  },
  description: "The web workspace for the Mecatl agent harness",
  icons: {
    apple: "/apple-touch-icon.png",
  },
  appleWebApp: {
    capable: true,
    statusBarStyle: "black-translucent",
    title: "Studio",
  },
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#03433e" },
    { media: "(prefers-color-scheme: dark)", color: "#02141b" },
  ],
  viewportFit: "cover",
  // App-like surface: pinch-zoom off (text sizes stay OS-controlled).
  maximumScale: 1,
  userScalable: false,
  // Android Chrome's default keeps the LAYOUT viewport fixed under the
  // on-screen keyboard (only the visual viewport shrinks), which buries the
  // docked composer behind it. resizes-content restores layout resize so
  // h-dvh — and the composer with it — shrinks above the keyboard. iOS
  // ignores this key.
  interactiveWidget: "resizes-content",
};

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  // The operator's palette pin (the MECATUI_THEME analogue): a browser with
  // no stored choice gets this palette. Read where the page renders — like
  // BRAND_NAME, a statically prerendered route captures it at build time.
  const defaultPalette = resolveDefaultPalette(process.env.BRAND_PALETTE);
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        {/* Blocking: puts the stored-or-default palette on <html> before the
            first paint, the way next-themes' own script does for .dark. */}
        <PaletteBootScript defaultPalette={defaultPalette} />
      </head>
      <body
        className={`${inter.variable} ${merriweather.variable} text-sm antialiased`}
      >
        <ServerProviders>
          <ClientProviders defaultPalette={defaultPalette}>
            {children}
          </ClientProviders>
        </ServerProviders>
      </body>
    </html>
  );
}
