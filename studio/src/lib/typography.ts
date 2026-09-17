import { cn } from "@/lib/utils";

/**
 * The reusable serif display treatment for page titles.
 *
 * Renders in Merriweather (the `font-serif` utility maps to
 * `--font-merriweather`, loaded in `layout.tsx`) coloured by the `--title`
 * token (the `text-title` utility, registered in `@theme inline`). Page
 * headers consume this so every page title is typeset identically.
 *
 * Exported as a helper so the treatment stays a single source of truth rather
 * than a class string copy-pasted per page.
 */
export function pageTitleClass(...extra: Parameters<typeof cn>): string {
  return cn("font-serif font-light text-title tracking-tight", ...extra);
}
