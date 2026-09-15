import { describe, expect, it } from "vitest";
import {
  CUSTOM_PROVIDER_API_FLAVORS,
  customProviderAuthSnippet,
  customProviderProbeURL,
  customProviderSettingsSnippet,
  KNOWN_AUTH_PROVIDERS,
  listAuthFileProviders,
  listSettingsProviders,
  RESERVED_CUSTOM_PROVIDER_IDS,
  removeAuthFileProvider,
  validCustomProviderBaseURL,
  validCustomProviderId,
  validProviderName,
} from "./provider-auth.mjs";

/**
 * The auth.yaml surgery the controller performs is the ONE place Studio
 * touches the credentials file, so its scope must be provable: removal cuts
 * exactly the named provider's block, inventory reports booleans only, and a
 * malformed or hostile file degrades to "not found" rather than a wider cut.
 */

const full = [
  "# operator notes stay",
  "providers:",
  "  openrouter:",
  "    api_key: sk-or-live",
  "  openai:",
  '    api_key: "sk-oai"',
  "  openai-codex:",
  "    oauth:",
  "      access_token: tok",
  "      account_id: acct",
  "      expires_at: 2026-01-01T00:00:00Z",
  "  anthropic:",
  "    # a comment inside the block",
  "    api_key: sk-ant",
  "other_top_level: true",
  "",
].join("\n");

describe("listAuthFileProviders", () => {
  it("lists every provider with a key-present boolean, never a value", () => {
    const providers = listAuthFileProviders(full);
    expect(providers).toEqual([
      { name: "openrouter", keyPresent: true },
      { name: "openai", keyPresent: true },
      { name: "openai-codex", keyPresent: true },
      { name: "anthropic", keyPresent: true },
    ]);
    // Structural: no field of any row can carry the credential text.
    expect(JSON.stringify(providers)).not.toMatch(/sk-|tok|acct/);
  });

  it("reports a missing, empty, quoted-empty, or commented key as absent", () => {
    const text = [
      "providers:",
      "  a:",
      "    api_key:",
      "  b:",
      '    api_key: ""',
      "  c:",
      "    api_key: # add me later",
      "  d:",
      "    base_url: https://example.test",
    ].join("\n");
    expect(listAuthFileProviders(text)).toEqual([
      { name: "a", keyPresent: false },
      { name: "b", keyPresent: false },
      { name: "c", keyPresent: false },
      { name: "d", keyPresent: false },
    ]);
  });

  it("finds an oauth access_token at any nesting depth", () => {
    const text = "providers:\n  codex:\n    oauth:\n      access_token: t\n";
    expect(listAuthFileProviders(text)).toEqual([
      { name: "codex", keyPresent: true },
    ]);
  });

  it("stops at the first dedented top-level key", () => {
    const text =
      "providers:\n  a:\n    api_key: x\nnot_a_provider:\n  b:\n    api_key: y\n";
    expect(listAuthFileProviders(text).map((p) => p.name)).toEqual(["a"]);
  });

  it("returns [] for an empty file or one with no providers block", () => {
    expect(listAuthFileProviders("")).toEqual([]);
    expect(listAuthFileProviders("models:\n  default: x\n")).toEqual([]);
  });
});

describe("removeAuthFileProvider", () => {
  it("removes a middle provider's whole nested block and nothing else", () => {
    const { text, removed } = removeAuthFileProvider(full, "openai-codex");
    expect(removed).toBe(true);
    expect(text).toBe(
      [
        "# operator notes stay",
        "providers:",
        "  openrouter:",
        "    api_key: sk-or-live",
        "  openai:",
        '    api_key: "sk-oai"',
        "  anthropic:",
        "    # a comment inside the block",
        "    api_key: sk-ant",
        "other_top_level: true",
        "",
      ].join("\n"),
    );
  });

  it("never removes a longer-named sibling on a prefix match", () => {
    // "openai" and "openai-codex" coexist; removing one must not touch the other.
    const { text } = removeAuthFileProvider(full, "openai");
    expect(text).toContain("  openai-codex:");
    expect(text).toContain("      access_token: tok");
    expect(text).not.toContain('    api_key: "sk-oai"');
    expect(removeAuthFileProvider(text, "openai").removed).toBe(false);
  });

  it("removes the first and last providers cleanly", () => {
    const first = removeAuthFileProvider(full, "openrouter");
    expect(first.text).not.toContain("sk-or-live");
    expect(first.text).toContain("  openai:");
    const last = removeAuthFileProvider(full, "anthropic");
    expect(last.text).not.toContain("sk-ant");
    // The comment inside the removed block goes with it.
    expect(last.text).not.toContain("a comment inside the block");
    expect(last.text).toContain("other_top_level: true");
  });

  it("keeps the providers: header when the last remaining provider is removed", () => {
    const lone = "providers:\n  openrouter:\n    api_key: sk\n";
    const { text, removed } = removeAuthFileProvider(lone, "openrouter");
    expect(removed).toBe(true);
    expect(text).toBe("providers:\n");
  });

  it("consumes an internal blank line but keeps a trailing gap", () => {
    const text = [
      "providers:",
      "  a:",
      "    api_key: x",
      "",
      "    base_url: https://example.test",
      "",
      "  b:",
      "    api_key: y",
    ].join("\n");
    const { text: next } = removeAuthFileProvider(text, "a");
    expect(next).toBe(["providers:", "", "  b:", "    api_key: y"].join("\n"));
  });

  it("stops at a comment at the providers indent (it may document the next entry)", () => {
    const text = [
      "providers:",
      "  a:",
      "    api_key: x",
      "  # b is the production key",
      "  b:",
      "    api_key: y",
    ].join("\n");
    const { text: next } = removeAuthFileProvider(text, "a");
    expect(next).toBe(
      [
        "providers:",
        "  # b is the production key",
        "  b:",
        "    api_key: y",
      ].join("\n"),
    );
  });

  it("returns removed:false without touching the text when the name is absent", () => {
    for (const missing of ["nope", "OPENAI", "openai ", "openai:"]) {
      const { text, removed } = removeAuthFileProvider(full, missing);
      expect(removed, missing).toBe(false);
      expect(text, missing).toBe(full);
    }
    expect(removeAuthFileProvider("", "openai")).toEqual({
      text: "",
      removed: false,
    });
  });

  it("only matches inside the providers block, never a lookalike elsewhere", () => {
    const text = [
      "backups:",
      "  openai:",
      "    api_key: keep-me",
      "providers:",
      "  openrouter:",
      "    api_key: sk",
    ].join("\n");
    const { text: next, removed } = removeAuthFileProvider(text, "openai");
    expect(removed).toBe(false);
    expect(next).toBe(text);
  });
});

describe("known provider registry", () => {
  it("mirrors the daemon's built-in set and never embeds a real key", () => {
    expect(KNOWN_AUTH_PROVIDERS.map((p) => p.name)).toEqual([
      "openrouter",
      "anthropic",
      "openai",
      "opencode",
      "openai-codex",
    ]);
    for (const provider of KNOWN_AUTH_PROVIDERS) {
      expect(provider.snippet).toMatch(/^providers:\n {2}[a-z0-9-]+:\n/);
      expect(provider.snippet).toMatch(/<YOUR_|<RFC3339_/);
    }
  });

  it("gates route parameters through the provider-name grammar", () => {
    for (const good of ["openai", "openai-codex", "a", "A_1"]) {
      expect(validProviderName(good), good).toBe(true);
    }
    for (const bad of ["", "-lead", "a/b", "a b", "a".repeat(65), "../x"]) {
      expect(validProviderName(bad), bad).toBe(false);
    }
  });
});

/**
 * Custom ("Custom gateway") provider helpers, ADR 0238: what the dialog
 * validates and emits must match mecated's strict operator-settings parse
 * (internal/adapter/permconfig/providers.go) byte-for-byte in shape, or the
 * copied snippet fails the daemon's startup.
 */
describe("custom provider id and base URL", () => {
  it("accepts the daemon's lower-case DNS-label-like grammar", () => {
    for (const good of ["g", "my-gateway", "a1", `a${"b".repeat(61)}c`]) {
      expect(validCustomProviderId(good), good).toBe(true);
    }
  });

  it("rejects bad shapes and every reserved built-in id", () => {
    for (const bad of [
      "",
      "My-Gateway",
      "-lead",
      "trail-",
      "under_score",
      "a".repeat(64),
      "a b",
    ]) {
      expect(validCustomProviderId(bad), bad).toBe(false);
    }
    // The daemon reserves the built-ins (reservedProviderIDs) — offering one
    // would emit a snippet mecated refuses.
    for (const reserved of RESERVED_CUSTOM_PROVIDER_IDS) {
      expect(validCustomProviderId(reserved), reserved).toBe(false);
    }
    expect(RESERVED_CUSTOM_PROVIDER_IDS).toContain("openai");
    expect(RESERVED_CUSTOM_PROVIDER_IDS).toContain("toolhive");
  });

  it("requires HTTPS with no userinfo, query, or fragment", () => {
    expect(validCustomProviderBaseURL("https://gw.example/v1")).toBe(true);
    expect(validCustomProviderBaseURL("https://gw.example:8443")).toBe(true);
    for (const bad of [
      "http://gw.example/v1",
      "https://user:pass@gw.example",
      "https://gw.example/v1?token=x",
      "https://gw.example/v1#frag",
      "not a url",
      "",
    ]) {
      expect(validCustomProviderBaseURL(bad), bad).toBe(false);
    }
  });
});

describe("custom provider snippets", () => {
  it("emits the exact settings providers: block the daemon parses", () => {
    const snippet = customProviderSettingsSnippet({
      id: "my-gateway",
      baseURL: "https://gw.example/v1",
      defaultModel: "org/model:free",
      apiFlavor: "openai-responses",
      authMethod: "api_key",
    });
    expect(snippet).toBe(
      [
        "providers:",
        "  my-gateway:",
        '    base_url: "https://gw.example/v1"',
        '    default_model: "org/model:free"',
        "    api_flavor: openai-responses",
        "    auth:",
        "      method: api_key",
        "",
      ].join("\n"),
    );
  });

  it("defaults any non-api_key auth to the daemon's none", () => {
    const snippet = customProviderSettingsSnippet({
      id: "open-gw",
      baseURL: "https://gw.example",
      defaultModel: "m",
      apiFlavor: "anthropic-messages",
      authMethod: "none",
    });
    expect(snippet).toContain("      method: none");
    expect(snippet).not.toContain("api_key");
  });

  it("emits an auth.yaml key block with a placeholder, never a value", () => {
    expect(customProviderAuthSnippet("my-gateway")).toBe(
      "providers:\n  my-gateway:\n    api_key: <YOUR_KEY>\n",
    );
  });

  it("round-trips: the emitted settings snippet lists back verbatim", () => {
    const snippet = customProviderSettingsSnippet({
      id: "round-trip",
      baseURL: "https://gw.example/v1",
      defaultModel: "m1",
      apiFlavor: "openai-chat-completions",
      authMethod: "none",
    });
    expect(listSettingsProviders(snippet)).toEqual([
      {
        name: "round-trip",
        baseURL: "https://gw.example/v1",
        defaultModel: "m1",
        apiFlavor: "openai-chat-completions",
        authMethod: "none",
      },
    ]);
  });
});

describe("listSettingsProviders", () => {
  const settings = [
    "# operator settings",
    "models:",
    "  default: x",
    "providers:",
    "  keyed-gw:",
    '    base_url: "https://keyed.example/v1"',
    "    default_model: m-keyed",
    "    api_flavor: openai-responses",
    "    auth:",
    "      method: api_key",
    "  open-gw:",
    "    base_url: https://open.example",
    "    default_model: m-open",
    "    api_flavor: anthropic-messages",
    "    auth:",
    "      method: none",
    "  implicit-gw:",
    "    base_url: https://implicit.example",
    "    default_model: m-implicit",
    "    api_flavor: openai-chat-completions",
    "permissions:",
    "  allow: []",
    "",
  ].join("\n");

  it("lists every custom definition with its non-secret shape", () => {
    expect(listSettingsProviders(settings)).toEqual([
      {
        name: "keyed-gw",
        baseURL: "https://keyed.example/v1",
        defaultModel: "m-keyed",
        apiFlavor: "openai-responses",
        authMethod: "api_key",
      },
      {
        name: "open-gw",
        baseURL: "https://open.example",
        defaultModel: "m-open",
        apiFlavor: "anthropic-messages",
        authMethod: "none",
      },
      {
        // No auth block = the daemon's default, none.
        name: "implicit-gw",
        baseURL: "https://implicit.example",
        defaultModel: "m-implicit",
        apiFlavor: "openai-chat-completions",
        authMethod: "none",
      },
    ]);
  });

  it("returns null when the text has no providers: key (the capture fold)", () => {
    // Distinct from []: mecated captures the section whole-block
    // first-non-nil across operator files, so a caller folding an imported
    // operator-settings.yaml over the user-global one needs the difference.
    expect(listSettingsProviders("models:\n  default: x\n")).toBeNull();
    expect(listSettingsProviders("")).toBeNull();
    expect(listSettingsProviders("providers: {}\n")).toEqual([]);
  });

  it("drops entries whose id the daemon would refuse", () => {
    const text = [
      "providers:",
      "  Bad_Name:",
      "    base_url: https://x.example",
      "  openai:", // reserved built-in
      "    base_url: https://y.example",
      "  fine:",
      "    base_url: https://z.example",
    ].join("\n");
    expect(listSettingsProviders(text)?.map((p) => p.name)).toEqual(["fine"]);
  });

  it("stops at the first dedented top-level key", () => {
    const text =
      "providers:\n  a-gw:\n    base_url: https://a.example\nother:\n  b-gw:\n    base_url: https://b.example\n";
    expect(listSettingsProviders(text)?.map((p) => p.name)).toEqual(["a-gw"]);
  });
});

describe("customProviderProbeURL", () => {
  it("builds a models-list probe per flavor against the configured base", () => {
    expect(
      customProviderProbeURL("openai-responses", "https://gw.example/v1"),
    ).toBe("https://gw.example/v1/models");
    expect(
      customProviderProbeURL(
        "openai-chat-completions",
        "https://gw.example/v1/",
      ),
    ).toBe("https://gw.example/v1/models");
    expect(
      customProviderProbeURL("anthropic-messages", "https://gw.example"),
    ).toBe("https://gw.example/models?limit=1");
  });

  it("refuses unknown flavors and unprobeable base URLs", () => {
    expect(customProviderProbeURL("grpc-exotic", "https://gw.example")).toBe(
      "",
    );
    expect(
      customProviderProbeURL("openai-responses", "http://gw.example"),
    ).toBe("");
    expect(customProviderProbeURL("openai-responses", "")).toBe("");
  });

  it("covers exactly the daemon's closed api_flavor enum", () => {
    expect(CUSTOM_PROVIDER_API_FLAVORS).toEqual([
      "openai-responses",
      "openai-chat-completions",
      "anthropic-messages",
    ]);
    for (const flavor of CUSTOM_PROVIDER_API_FLAVORS) {
      expect(
        customProviderProbeURL(flavor, "https://gw.example"),
        flavor,
      ).not.toBe("");
    }
  });
});
