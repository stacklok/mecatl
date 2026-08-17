"use client";

import { ThemeProvider as NextThemesProvider } from "next-themes";
import type { ComponentProps } from "react";

// Matches the console's provider configuration exactly: the `.dark` CLASS is the
// dark-mode signal (paired with `@custom-variant dark (&:is(.dark *))` in
// globals.css), light is the default, and transitions are suppressed on switch
// so a theme change does not animate every token at once.
export function ThemeProvider({
  children,
  ...props
}: ComponentProps<typeof NextThemesProvider>) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="light"
      disableTransitionOnChange
      {...props}
    >
      {children}
    </NextThemesProvider>
  );
}
