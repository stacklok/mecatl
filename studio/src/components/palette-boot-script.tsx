import { buildPaletteBootScript } from "@/lib/palettes";

/**
 * The blocking inline script the root layout places in `<head>`: it copies
 * the stored palette (or the deployment default) onto `<html>` while the HTML
 * is still parsing, before the first paint — the anti-flash technique
 * next-themes uses for the light/dark class. Permitted by the CSP
 * (`script-src 'self' 'unsafe-inline'`, next.config.ts).
 */
export function PaletteBootScript({
  defaultPalette,
}: {
  defaultPalette: string;
}) {
  return (
    <script
      // biome-ignore lint/security/noDangerouslySetInnerHtml: a constant program; the only interpolations are JSON.stringify'd catalogue ids and the validated default (see buildPaletteBootScript)
      dangerouslySetInnerHTML={{
        __html: buildPaletteBootScript(defaultPalette),
      }}
    />
  );
}
