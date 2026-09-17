"use client";

import { useCustomPalettes } from "@/lib/custom-palettes";
import { renderPaletteCSS } from "@/lib/palette-schema";

export const CUSTOM_PALETTE_STYLE_ID = "studio-custom-palettes";

/**
 * Mounts the stylesheet for every custom palette (operator + user) as one
 * <style> element, rendered in place — AFTER globals.css in document order,
 * which is what lets a `[data-palette="custom:…"]` block win its specificity
 * tie with `:root` / `.dark`. Mounting it is also what starts the one-per-page
 * `/api/palettes` fetch.
 *
 * The CSS is set through dangerouslySetInnerHTML on purpose: React would
 * HTML-escape a text child (turning `"` into `&quot;` inside the selectors),
 * and the string needs no escaping — it is built by `renderPaletteCSS` from
 * allowlisted token names and colour values that passed `isSafeCssColor`,
 * a grammar with no `<`, `;`, `{`, `}` or `*`, so nothing in it can close
 * the element or a block. That validator is the injection boundary, not
 * this component.
 */
export function CustomPaletteStyles() {
  const { operator, user } = useCustomPalettes();
  const css = [...operator, ...user]
    .map(renderPaletteCSS)
    .filter((rule) => rule !== "")
    .join("\n");
  if (css === "") return null;
  return (
    <style
      id={CUSTOM_PALETTE_STYLE_ID}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: built only from allowlisted token names and isSafeCssColor-validated values (see renderPaletteCSS); text children would be HTML-escaped
      dangerouslySetInnerHTML={{ __html: css }}
    />
  );
}
