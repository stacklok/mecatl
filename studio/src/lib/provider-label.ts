import { KNOWN_AUTH_PROVIDERS } from "@/lib/provider-auth.mjs";

/** The plain name for a provider id: the built-in label when there is
 *  one, the gateway's own name, else the id as configured. Shared by every
 *  surface that names a provider to the user, so a raw id like
 *  `openrouter` never reaches the page. */
export function providerLabel(name: string): string {
  if (name === "toolhive") return "ToolHive gateway";
  return (
    KNOWN_AUTH_PROVIDERS.find((entry) => entry.name === name)?.label ?? name
  );
}
