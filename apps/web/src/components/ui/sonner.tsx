// SPDX-License-Identifier: Apache-2.0

import { Toaster as Sonner, type ToasterProps } from "sonner";

import { useTheme } from "../../lib/theme";

/**
 * The app-wide toast host, mounted once at the root. Mirrors the prototype's
 * `ThemedToaster` (`components/app-providers.tsx`): the same placement and
 * timing, with the theme taken from Studio's appearance store instead of
 * next-themes so toasts follow the light/dark choice live.
 */
function Toaster(props: ToasterProps) {
  const { effectiveTheme } = useTheme();
  return (
    <Sonner
      theme={effectiveTheme}
      duration={2000}
      position="bottom-right"
      offset={{ top: 50 }}
      closeButton
      {...props}
    />
  );
}

export { Toaster };
