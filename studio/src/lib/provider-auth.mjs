// Pure helpers over mecated's auth.yaml (`providers:` block) shared by the
// managed-mode controller and its vitest suite. Everything here is a LINE
// SCAN, never a YAML parse, mirroring listConfiguredProviderNames in
// scripts/local-controller.mjs — and, deliberately, nothing in this module
// can RETURN a credential: inventory reports booleans, and removal returns
// the file with a block cut out. Reading a key VALUE (for the controller's
// server-side key test) lives in the controller script only, never in a
// module the browser bundle may import (Studio rule 3: credentials never
// cross the browser/controller boundary).

/**
 * The BUILT-IN provider kinds mecated's auth.yaml understands, mirrored from
 * the Go side (`internal/cliconfig/cliconfig.go` knownAuthProviders:
 * "anthropic", "openai", "openrouter", "opencode", "openai-codex"). That set
 * is no longer closed: since ADR 0238 an operator can define CUSTOM providers
 * in the operator settings.yaml `providers:` section, and the daemon's
 * permitted auth.yaml ids become knownAuthProviders PLUS those custom ids
 * (`internal/cliconfig` ResolveProviderCredentials). This registry therefore
 * lists only the guided-add BUILT-INS — custom gateways ride the "Custom
 * gateway" flow (validCustomProviderId + the snippet builders below) and are
 * discovered from the settings file, not from this list. There is no
 * machine-readable source the controller could read at runtime, so the
 * built-ins are pinned here with this citation; extend the list when the
 * daemon's BUILT-IN set grows. Each entry carries the exact auth.yaml
 * snippet the guided-add dialog shows — with a `<YOUR_KEY>` placeholder,
 * never a real value. `testable` marks kinds the controller can key-test
 * with one cheap authenticated call ("toolhive" is absent: it is
 * auto-detected from ToolHive's own config and never appears in auth.yaml).
 */
export const KNOWN_AUTH_PROVIDERS = [
  {
    name: "openrouter",
    label: "OpenRouter",
    testable: true,
    snippet: "providers:\n  openrouter:\n    api_key: <YOUR_KEY>\n",
    note: "Create a key at openrouter.ai/keys.",
  },
  {
    name: "anthropic",
    label: "Anthropic",
    testable: true,
    snippet: "providers:\n  anthropic:\n    api_key: <YOUR_KEY>\n",
    note: "An Anthropic API key (console.anthropic.com).",
  },
  {
    name: "openai",
    label: "OpenAI",
    testable: true,
    snippet: "providers:\n  openai:\n    api_key: <YOUR_KEY>\n",
    note: "An OpenAI API key (platform.openai.com).",
  },
  {
    name: "opencode",
    label: "OpenCode Go",
    testable: true,
    snippet: "providers:\n  opencode:\n    api_key: <YOUR_KEY>\n",
    note: "An OpenCode Go gateway key.",
  },
  {
    name: "openai-codex",
    label: "OpenAI Codex subscription",
    testable: false,
    // The oauth block is a manually supplied ChatGPT Codex subscription token
    // (docs/usage.md "OpenAI Codex subscription"), not a long-lived API key —
    // which is also why the controller refuses to key-test it: a merely
    // EXPIRED token would read as "rejected" and send the operator chasing a
    // non-problem.
    snippet:
      "providers:\n  openai-codex:\n    oauth:\n      access_token: <YOUR_ACCESS_TOKEN>\n      account_id: <YOUR_ACCOUNT_ID>\n      expires_at: <RFC3339_EXPIRY>\n",
    note: "A manually supplied ChatGPT Codex subscription token; see the mecatl usage docs for the copy-in steps.",
  },
];

/** The daemon's provider-name grammar as the controller's routes accept it. */
export function validProviderName(name) {
  return /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(name);
}

// ── Custom ("Custom gateway") provider helpers — ADR 0238 ───────────────────
// Operator-defined providers live in the operator settings.yaml `providers:`
// section (NON-secret by design: id, base_url, default_model, api_flavor,
// auth.method) with any api_key in auth.yaml under `providers.<id>.api_key`.
// Everything here mirrors the daemon's strict parse
// (`internal/adapter/permconfig/providers.go`) so a snippet Studio emits
// never fails mecated's startup validation — and, like the rest of this
// module, none of it can return or carry a credential.

/** The daemon's closed `api_flavor` enum, in the order the dialog offers. */
export const CUSTOM_PROVIDER_API_FLAVORS = [
  "openai-responses",
  "openai-chat-completions",
  "anthropic-messages",
];

/** Ids the daemon reserves for built-ins — a custom entry may not take them
 *  (`internal/adapter/permconfig/providers.go` reservedProviderIDs). */
export const RESERVED_CUSTOM_PROVIDER_IDS = [
  "anthropic",
  "mock",
  "openai",
  "openai-codex",
  "openrouter",
  "opencode",
  "toolhive",
];

/** The daemon's custom-provider id grammar: a lower-case DNS-label-like name
 *  (`providerIDPattern`), minus the reserved built-in ids. */
export function validCustomProviderId(id) {
  return (
    /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(String(id ?? "")) &&
    !RESERVED_CUSTOM_PROVIDER_IDS.includes(id)
  );
}

/** The daemon's base-URL rule: HTTPS with a host and no userinfo, query, or
 *  fragment (`validProviderURL`). */
export function validCustomProviderBaseURL(raw) {
  let url;
  try {
    url = new URL(String(raw ?? ""));
  } catch {
    return false;
  }
  return (
    url.protocol === "https:" &&
    url.hostname !== "" &&
    !url.username &&
    !url.password &&
    !url.search &&
    !url.hash
  );
}

/** Double-quotes a scalar for YAML (a JSON string is a valid YAML flow
 *  scalar), so model ids and URLs with `:` or spaces survive the paste. */
const yamlQuote = (value) => JSON.stringify(String(value ?? ""));

/**
 * The operator settings.yaml `providers:` block for one custom provider —
 * the exact shape mecated's strict parse accepts (ADR 0238). Non-secret by
 * construction: the credential, if any, goes in auth.yaml via
 * customProviderAuthSnippet.
 */
/**
 * The settings.yaml provider_overrides block routing a BUILT-IN provider
 * through a gateway/proxy base URL (ADR 0238's settings equivalent of the
 * --*-base-url flags). Operator-tier only, like the providers: section.
 * Returns "" when the URL fails the same validation custom gateways use.
 */
export function providerOverrideSnippet(name, baseURL) {
  if (!name || !validCustomProviderBaseURL(baseURL)) return "";
  return `provider_overrides:\n  ${name}:\n    base_url: "${baseURL.trim()}"\n`;
}

export function customProviderSettingsSnippet({
  id,
  baseURL,
  defaultModel,
  apiFlavor,
  authMethod,
}) {
  return [
    "providers:",
    `  ${id}:`,
    `    base_url: ${yamlQuote(baseURL)}`,
    `    default_model: ${yamlQuote(defaultModel)}`,
    `    api_flavor: ${apiFlavor}`,
    "    auth:",
    `      method: ${authMethod === "api_key" ? "api_key" : "none"}`,
    "",
  ].join("\n");
}

/** The auth.yaml key block for an api_key custom provider — a `<YOUR_KEY>`
 *  placeholder, never a real value (Studio rule 3), matching the built-in
 *  snippets above. */
export function customProviderAuthSnippet(id) {
  return `providers:\n  ${id}:\n    api_key: <YOUR_KEY>\n`;
}

/**
 * The one bounded key-probe URL for a custom provider, or "" when its flavor
 * or base URL cannot be probed. Both OpenAI flavors and Anthropic Messages
 * serve a models listing relative to the base URL, which is also what the
 * daemon's own flavor-specific live lister calls — so a 200 here means the
 * key works against the endpoint mecated will actually use. No credential
 * enters or leaves this function: the controller attaches the stored key to
 * the returned URL server-side.
 */
export function customProviderProbeURL(apiFlavor, baseURL) {
  if (!validCustomProviderBaseURL(baseURL)) return "";
  const base = String(baseURL).replace(/\/+$/, "");
  switch (apiFlavor) {
    case "openai-responses":
    case "openai-chat-completions":
      return `${base}/models`;
    case "anthropic-messages":
      return `${base}/models?limit=1`;
    default:
      return "";
  }
}

/**
 * The custom provider definitions in an operator settings.yaml text's
 * top-level `providers:` section — ids and NON-secret shape only (the
 * section never holds a credential; keys live in auth.yaml). A line scan,
 * never a YAML parse, like everything else in this module. Entries whose id
 * fails the daemon's grammar (or takes a reserved built-in name) are
 * dropped, mirroring mecated's strict parse rejecting the whole section —
 * listing a provider the daemon refused would lie.
 *
 * Returns NULL when the text has no top-level `providers:` key at all —
 * distinct from an empty array — because mecated captures the section
 * whole-block first-non-nil across its operator-tier files, and a caller
 * folding several sources needs the same distinction.
 *
 * @param {string} text
 * @returns {{name: string, baseURL: string, defaultModel: string,
 *            apiFlavor: string, authMethod: string}[] | null}
 */
export function listSettingsProviders(text) {
  const lines = String(text ?? "").split("\n");
  const providersAt = lines.findIndex((line) =>
    /^providers:\s*(\{\s*\}\s*)?(#.*)?$/.test(line),
  );
  if (providersAt === -1) return null;
  const providers = [];
  let current = null;
  let inAuth = false;
  for (const line of lines.slice(providersAt + 1)) {
    if (/^\s*#/.test(line) || line.trim() === "") continue;
    if (/^\S/.test(line)) break; // dedented past the providers block
    const key = line.match(/^ {2}([A-Za-z0-9][A-Za-z0-9_-]*):\s*(#.*)?$/);
    if (key) {
      current = {
        name: key[1],
        baseURL: "",
        defaultModel: "",
        apiFlavor: "",
        authMethod: "none",
      };
      providers.push(current);
      inAuth = false;
      continue;
    }
    if (!current) continue;
    const field = line.match(/^\s+([a-z_]+):\s*(.*)$/);
    if (!field) continue;
    const value = settingsScalar(field[2]);
    switch (field[1]) {
      case "auth":
        inAuth = true;
        break;
      case "method":
        if (inAuth) current.authMethod = value || "none";
        break;
      case "base_url":
        current.baseURL = value;
        inAuth = false;
        break;
      case "default_model":
        current.defaultModel = value;
        inAuth = false;
        break;
      case "api_flavor":
        current.apiFlavor = value;
        inAuth = false;
        break;
      default:
        break;
    }
  }
  return providers.filter((provider) => validCustomProviderId(provider.name));
}

/** A settings scalar as a YAML reader would see it: trimmed, matched quotes
 *  stripped, a comment-only remainder read as "". */
function settingsScalar(raw) {
  let value = (raw ?? "").trim();
  if (value.startsWith("#")) return "";
  const quote = value[0];
  if ((quote === '"' || quote === "'") && value.endsWith(quote)) {
    value = value.slice(1, -1);
  }
  return value;
}

const providersHeader = /^providers:\s*$/;
const providerKeyLine = /^ {2}([A-Za-z0-9_-]+):/;

/** Strips matched surrounding quotes from a scalar, mirroring what a YAML
 *  reader would see. Returns "" for a comment-only remainder. */
function scalarPresent(raw) {
  let value = (raw ?? "").trim();
  if (value === "" || value.startsWith("#")) return false;
  const quote = value[0];
  if ((quote === '"' || quote === "'") && value.endsWith(quote)) {
    value = value.slice(1, -1).trim();
  }
  return value !== "";
}

/**
 * Names + key-health BOOLEANS of the providers configured in an auth.yaml
 * text — never their values. A provider "has a key" when its block carries a
 * non-empty `api_key:` scalar or (openai-codex's shape) a non-empty
 * `access_token:` anywhere in its nested block.
 *
 * @param {string} text
 * @returns {{name: string, keyPresent: boolean}[]}
 */
export function listAuthFileProviders(text) {
  const lines = String(text ?? "").split("\n");
  const providersAt = lines.findIndex((line) => providersHeader.test(line));
  if (providersAt === -1) return [];
  const providers = [];
  let current = null;
  for (const line of lines.slice(providersAt + 1)) {
    if (/^\s*#/.test(line) || line.trim() === "") continue;
    const key = line.match(providerKeyLine);
    if (key) {
      current = { name: key[1], keyPresent: false };
      providers.push(current);
      continue;
    }
    if (/^\S/.test(line)) break; // dedented past the providers block
    if (!current) continue; // deeper content before any provider key: skip
    const credential = line.match(/^\s+(?:api_key|access_token):(.*)$/);
    if (credential && scalarPresent(credential[1])) current.keyPresent = true;
  }
  return providers;
}

/**
 * Removes the named provider's block from an auth.yaml text: the exact
 * 2-space-indented `<name>:` line inside the top-level `providers:` block
 * plus every line that provably belongs to it (deeper-indented lines,
 * including nested blocks and indented comments; blank lines only when more
 * of the block follows them). Everything else is preserved byte-for-byte —
 * comments at the providers level, sibling providers, unrelated top-level
 * keys, trailing whitespace. The name match is exact (`===` on the captured
 * key), so removing "openai" can never eat "openai-codex".
 *
 * The `providers:` header itself is left in place even when the last entry
 * is removed — an empty `providers:` key is valid YAML the daemon reads as
 * "no providers", and keeping it means the operator's file keeps its shape.
 *
 * @param {string} text
 * @param {string} name
 * @returns {{text: string, removed: boolean}}
 */
export function removeAuthFileProvider(text, name) {
  const source = String(text ?? "");
  const lines = source.split("\n");
  const providersAt = lines.findIndex((line) => providersHeader.test(line));
  if (providersAt === -1) return { text: source, removed: false };

  let start = -1;
  for (let i = providersAt + 1; i < lines.length; i += 1) {
    const line = lines[i];
    if (/^\S/.test(line)) break; // dedented past the providers block
    const key = line.match(providerKeyLine);
    if (key && key[1] === name) {
      start = i;
      break;
    }
  }
  if (start === -1) return { text: source, removed: false };

  // Walk the block: consume deeper-indented lines outright; consume a run of
  // blank lines ONLY when a deeper-indented line follows it (a blank gap
  // before the next sibling or a dedent stays in the file). A comment at the
  // providers indent (or shallower) may document the NEXT entry, so it ends
  // the block conservatively.
  let end = start + 1;
  while (end < lines.length) {
    const line = lines[end];
    if (line.trim() === "") {
      let ahead = end + 1;
      while (ahead < lines.length && lines[ahead].trim() === "") ahead += 1;
      if (ahead < lines.length && /^(?: {3,}|\t)/.test(lines[ahead])) {
        end = ahead + 1;
        continue;
      }
      break;
    }
    if (/^(?: {3,}|\t)/.test(line)) {
      end += 1;
      continue;
    }
    break;
  }
  return {
    text: [...lines.slice(0, start), ...lines.slice(end)].join("\n"),
    removed: true,
  };
}
