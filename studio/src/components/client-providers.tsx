"use client";

import { ThemeProvider, useTheme } from "next-themes";
import { type ReactNode, Suspense } from "react";
import { Toaster } from "sonner";
import { CustomPaletteStyles } from "@/components/custom-palette-styles";
import { PaletteProvider } from "@/components/palette-provider";
import { useUiScale } from "@/lib/profile-preferences";

/** Applies the stored interface-scale preference to the root on load. */
function UiScaleInit() {
  useUiScale();
  return null;
}

interface ClientProvidersProps {
  children: ReactNode;
  /** The deployment's palette pin (`BRAND_PALETTE`), resolved by the root layout. */
  defaultPalette?: string;
}

function ThemedToaster() {
  const { resolvedTheme } = useTheme();
  return (
    <Toaster
      theme={resolvedTheme as "light" | "dark" | undefined}
      duration={2000}
      position="bottom-right"
      offset={{ top: 50 }}
      closeButton
    />
  );
}

export function ClientProviders({
  children,
  defaultPalette,
}: ClientProvidersProps) {
  return (
    <ThemeProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
    >
      {/* Palette is the second appearance axis (data-palette on <html>),
          independent of next-themes' light/dark class. */}
      <PaletteProvider defaultPalette={defaultPalette}>
        {/* Custom palettes (user + STUDIO_PALETTE_DIR) need their CSS in the
            document; this is the one app-wide instance that loads them. */}
        <CustomPaletteStyles />
        <Suspense fallback={null}>
          <UiScaleInit />
          {children}
          <ThemedToaster />
        </Suspense>
      </PaletteProvider>
    </ThemeProvider>
  );
}
