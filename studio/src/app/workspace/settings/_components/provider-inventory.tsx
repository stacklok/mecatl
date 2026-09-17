import type { HarnessProviderInfo } from "@/lib/harness/client";

/**
 * Grouping of the controller's provider rows for the Providers card. Every
 * row carries names, booleans and fixed strings — no credential value exists
 * anywhere in these payloads (Studio rule 3).
 */

/** Splits the controller's rows into the three groups the section renders:
 *  configured providers (built-in with a block or env var, plus custom
 *  definitions), the ToolHive external row, and the unconfigured built-in
 *  kinds an operator could add. An older controller (no class fields) lands
 *  every row in `configured`. */
export function groupProviderRows(rows: HarnessProviderInfo[]): {
  configured: HarnessProviderInfo[];
  toolhive: HarnessProviderInfo | null;
  available: HarnessProviderInfo[];
} {
  const toolhive =
    rows.find((row) => row.name === "toolhive" || row.class === "external") ??
    null;
  const rest = rows.filter((row) => row !== toolhive);
  return {
    configured: rest.filter((row) => row.configured !== false),
    toolhive,
    available: rest.filter((row) => row.configured === false),
  };
}
