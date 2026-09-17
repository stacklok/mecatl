import { describe, expect, it } from "vitest";
import {
  CUSTOM_PROVIDER_API_FLAVORS,
  customProviderProbeURL,
  customProviderSettingsSnippet,
  describeProviderRow,
  describeToolhiveRow,
  KNOWN_AUTH_PROVIDERS,
  listAuthFileProviders,
  listSettingsProviders,
  PROVIDER_CLASSES,
  PROVIDER_ENV_KEYS,
  RESERVED_CUSTOM_PROVIDER_IDS,
  removeAuthFileProvider,
  removeAuthFileProviderKey,
  removeSettingsProvider,
  upsertSettingsProvider,
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

/**
 * The richer inventory row (`providers status` parity): class, auth method
 * and state — including "the environment shadows auth.yaml" — the default
 * model, and the next recovery step, all from booleans and non-secret
 * definition fields. The matrix pins the exact strings the UI renders, and
 * the last case proves the shaper has no channel a credential could ride.
 */
describe("describeProviderRow", () => {
  it("names one env var per api-key built-in and none for openai-codex", () => {
    expect(PROVIDER_ENV_KEYS).toEqual({
      openrouter: "OPENROUTER_API_KEY",
      openai: "OPENAI_API_KEY",
      anthropic: "ANTHROPIC_API_KEY",
      opencode: "OPENCODE_API_KEY",
    });
    expect(Object.keys(PROVIDER_ENV_KEYS)).not.toContain("openai-codex");
    expect(PROVIDER_CLASSES).toEqual(["built-in", "custom", "external"]);
  });

  it("built-in with a file key: configured, api_key, set-as-active next", () => {
    const row = describeProviderRow({
      kind: "openrouter",
      hasBlock: true,
      keyPresent: true,
    });
    expect(row).toMatchObject({
      name: "openrouter",
      class: "built-in",
      authMethod: "api_key",
      configured: true,
      keyPresent: true,
      envShadowed: false,
      authState: "configured",
      source: "auth.yaml",
      nextStep: "set as active to use it",
      active: false,
    });
  });

  it("built-in with a block but no key: not configured, add-a-key next", () => {
    const row = describeProviderRow({
      kind: "anthropic",
      hasBlock: true,
      keyPresent: false,
    });
    expect(row.authState).toBe("not configured");
    expect(row.nextStep).toBe("add an API key to auth.yaml");
    expect(row.configured).toBe(true);
    expect(row.keyPresent).toBe(false);
  });

  it("built-in with NO block at all (the `status PROVIDER` unconfigured kind)", () => {
    const row = describeProviderRow({ kind: "openai" });
    expect(row).toMatchObject({
      class: "built-in",
      configured: false,
      keyPresent: false,
      authState: "not configured",
      nextStep: "add an API key to auth.yaml",
      source: "",
    });
  });

  it("env-shadowed built-in: the environment wins over auth.yaml for mecated", () => {
    const both = describeProviderRow({
      kind: "openai",
      hasBlock: true,
      keyPresent: true,
      envShadowed: true,
    });
    expect(both.envShadowed).toBe(true);
    expect(both.authState).toBe("configured (environment shadows auth.yaml)");
    expect(both.source).toBe("auth.yaml + environment");
    // keyPresent stays the FILE truth (the key test reads the file).
    expect(both.keyPresent).toBe(true);

    const envOnly = describeProviderRow({ kind: "openai", envShadowed: true });
    expect(envOnly.configured).toBe(true);
    expect(envOnly.keyPresent).toBe(false);
    expect(envOnly.authState).toBe("configured (environment)");
    expect(envOnly.source).toBe("environment");
    // A credential exists (in the environment), so the next step is use.
    expect(envOnly.nextStep).toBe("set as active to use it");
  });

  it("openai-codex is an oauth manual token and never env-shadowed", () => {
    const row = describeProviderRow({
      kind: "openai-codex",
      hasBlock: true,
      keyPresent: true,
      envShadowed: true, // ignored: there is no env var for the token
    });
    expect(row.authMethod).toBe("oauth");
    expect(row.authState).toBe("manual token");
    expect(row.envShadowed).toBe(false);
    expect(
      describeProviderRow({ kind: "openai-codex", hasBlock: true }).nextStep,
    ).toBe("add the subscription token to auth.yaml");
  });

  it("orders the next step: ready when active, restart when the daemon has not loaded the key", () => {
    expect(
      describeProviderRow({
        kind: "openrouter",
        hasBlock: true,
        keyPresent: true,
        active: true,
        inDaemonInventory: false,
      }).nextStep,
    ).toBe("ready to use");
    expect(
      describeProviderRow({
        kind: "openrouter",
        hasBlock: true,
        keyPresent: true,
        inDaemonInventory: false,
      }).nextStep,
    ).toBe("restart the daemon to load the key");
    // Unknown inventory (daemon down / on the mock) offers no restart hint.
    expect(
      describeProviderRow({
        kind: "openrouter",
        hasBlock: true,
        keyPresent: true,
        inDaemonInventory: null,
      }).nextStep,
    ).toBe("set as active to use it");
    // No credential outranks everything else.
    expect(
      describeProviderRow({
        kind: "openrouter",
        hasBlock: true,
        active: true,
        inDaemonInventory: false,
      }).nextStep,
    ).toBe("add an API key to auth.yaml");
  });

  it("carries the daemon default model for a built-in when one is saved", () => {
    expect(
      describeProviderRow({
        kind: "openrouter",
        hasBlock: true,
        keyPresent: true,
        defaultModel: "anthropic/claude",
      }).defaultModel,
    ).toBe("anthropic/claude");
    expect(describeProviderRow({ kind: "openrouter" }).defaultModel).toBe("");
  });

  it("custom api_key gateway with and without its auth.yaml key", () => {
    const definition = {
      name: "my-gateway",
      baseURL: "https://gw.example/v1",
      defaultModel: "org/model",
      apiFlavor: "openai-responses",
      authMethod: "api_key",
    };
    const keyed = describeProviderRow({
      kind: "my-gateway",
      definition,
      hasBlock: true,
      keyPresent: true,
    });
    expect(keyed).toMatchObject({
      class: "custom",
      authMethod: "api_key",
      configured: true,
      keyPresent: true,
      authState: "configured",
      defaultModel: "org/model",
      source: "settings.yaml + auth.yaml",
      nextStep: "set as active to use it",
    });
    const unkeyed = describeProviderRow({ kind: "my-gateway", definition });
    expect(unkeyed.keyPresent).toBe(false);
    expect(unkeyed.authState).toBe("not configured");
    expect(unkeyed.source).toBe("settings.yaml");
    expect(unkeyed.nextStep).toBe("add an API key to auth.yaml");
    // A custom gateway has no env var, so it is never env-shadowed.
    expect(
      describeProviderRow({ kind: "my-gateway", definition, envShadowed: true })
        .envShadowed,
    ).toBe(false);
  });

  it("custom keyless gateway: auth not required, credential satisfied", () => {
    const row = describeProviderRow({
      kind: "open-gw",
      definition: {
        name: "open-gw",
        baseURL: "https://gw.example",
        defaultModel: "",
        apiFlavor: "openai-chat-completions",
        authMethod: "none",
      },
    });
    expect(row.authMethod).toBe("none");
    expect(row.authState).toBe("not required");
    expect(row.keyPresent).toBe(true);
    expect(row.nextStep).toBe("set as active to use it");
    expect(row.defaultModel).toBe("");
  });

  it("an auth.yaml block naming neither a built-in nor a custom id is class unknown", () => {
    expect(
      describeProviderRow({ kind: "mystery", hasBlock: true, keyPresent: true })
        .class,
    ).toBe("unknown");
  });

  it("has no channel a credential value could travel through", () => {
    // Every argument is a flag, an id, a model name or a URL; a caller that
    // passes a key-shaped extra property finds it nowhere in the row.
    const row = describeProviderRow({
      kind: "openrouter",
      hasBlock: true,
      keyPresent: true,
      // @ts-expect-error — not an accepted input, and must not leak
      apiKey: "sk-or-live-SECRET",
      definition: null,
    });
    const rendered = JSON.stringify(row);
    expect(rendered).not.toContain("SECRET");
    expect(rendered).not.toContain("sk-or");
    for (const value of Object.values(row)) {
      expect(["string", "boolean"]).toContain(typeof value);
    }
  });
});

describe("describeToolhiveRow", () => {
  it("reachable: an external row that can be set as active", () => {
    const row = describeToolhiveRow({
      reachable: true,
      baseURL: "http://127.0.0.1:14000/v1",
      thvOnPath: true,
    });
    expect(row).toMatchObject({
      name: "toolhive",
      class: "external",
      authMethod: "external",
      configured: true,
      keyPresent: true,
      reachable: true,
      thvOnPath: true,
      authState: "gateway reachable",
      nextStep: "set as active to use it",
      source: "thv llm proxy",
      baseURL: "http://127.0.0.1:14000/v1",
    });
    expect(
      describeToolhiveRow({ reachable: true, active: true }).nextStep,
    ).toBe("ready to use");
  });

  it("unreachable: delegates to thv llm, or to installing it", () => {
    const startable = describeToolhiveRow({ thvOnPath: true });
    expect(startable.configured).toBe(false);
    expect(startable.authState).toBe("gateway not reachable");
    expect(startable.nextStep).toBe("start the gateway (thv llm proxy start)");
    const missing = describeToolhiveRow({ thvOnPath: false });
    expect(missing.nextStep).toBe(
      "install ToolHive, then run thv llm proxy start",
    );
  });

  it("detection switched off in daemon defaults out-ranks reachability", () => {
    const row = describeToolhiveRow({ reachable: true, enabled: false });
    expect(row.configured).toBe(false);
    expect(row.authState).toBe("detection switched off");
    expect(row.nextStep).toBe("enable ToolHive detection in Daemon defaults");
  });
});

/**
 * The controller now WRITES the non-secret custom provider definition into
 * the user-global settings.yaml (`providers add`) and removes it
 * (`providers remove`), and cuts only the key line for `providers logout`.
 * Each editor is a line-range edit that must prove its scope the way
 * removeAuthFileProvider does: exact id match, byte-preserving everything
 * it did not touch, refusing rather than guessing, and never carrying a
 * credential.
 */
const gateway = {
  id: "my-gateway",
  baseURL: "https://gw.example/v1",
  defaultModel: "org/model:latest",
  apiFlavor: "openai-responses",
  authMethod: "api_key",
};

const gatewayEntry = [
  "  my-gateway:",
  '    base_url: "https://gw.example/v1"',
  '    default_model: "org/model:latest"',
  "    api_flavor: openai-responses",
  "    auth:",
  "      method: api_key",
];

describe("upsertSettingsProvider", () => {
  it("appends a providers: block to an empty file", () => {
    const { text, written } = upsertSettingsProvider("", gateway);
    expect(written).toBe(true);
    expect(text).toBe(["providers:", ...gatewayEntry, ""].join("\n"));
    // What is written is exactly what the guided-copy snippet showed.
    expect(text).toBe(customProviderSettingsSnippet(gateway));
  });

  it("appends after unrelated top-level keys, adding the missing final newline once", () => {
    const original = "models:\n  default: x\npermissions:\n  allow: []";
    const { text, written } = upsertSettingsProvider(original, gateway);
    expect(written).toBe(true);
    expect(text).toBe(
      `${original}\n${["providers:", ...gatewayEntry, ""].join("\n")}`,
    );
    expect(listSettingsProviders(text)).toEqual([
      {
        name: "my-gateway",
        baseURL: "https://gw.example/v1",
        defaultModel: "org/model:latest",
        apiFlavor: "openai-responses",
        authMethod: "api_key",
      },
    ]);
  });

  it("inserts after the last entry of an existing providers: block, keeping the gap before the next key", () => {
    const original = [
      "providers:",
      "  # the production gateway",
      "  first-gw:",
      "    base_url: https://first.example",
      "    default_model: m",
      "    api_flavor: anthropic-messages",
      "",
      "permissions:",
      "  allow: []",
      "",
    ].join("\n");
    const { text, written } = upsertSettingsProvider(original, {
      ...gateway,
      authMethod: "none",
    });
    expect(written).toBe(true);
    expect(text).toBe(
      [
        "providers:",
        "  # the production gateway",
        "  first-gw:",
        "    base_url: https://first.example",
        "    default_model: m",
        "    api_flavor: anthropic-messages",
        ...gatewayEntry.slice(0, -1),
        "      method: none",
        "",
        "permissions:",
        "  allow: []",
        "",
      ].join("\n"),
    );
    expect(listSettingsProviders(text)?.map((p) => p.name)).toEqual([
      "first-gw",
      "my-gateway",
    ]);
  });

  it("turns a flow-style empty section (providers: {}) back into a block", () => {
    const { text, written } = upsertSettingsProvider(
      "providers: {} # none yet\nother: 1\n",
      gateway,
    );
    expect(written).toBe(true);
    expect(text).toBe(
      ["providers: # none yet", ...gatewayEntry, "other: 1", ""].join("\n"),
    );
  });

  it("refuses a duplicate id without touching the text", () => {
    const original = customProviderSettingsSnippet(gateway);
    const result = upsertSettingsProvider(original, {
      ...gateway,
      baseURL: "https://other.example",
    });
    expect(result.written).toBe(false);
    expect(result.text).toBe(original);
    expect(result.reason).toMatch(/already defined/);
    // Exact match: a prefix-sharing id is NOT a duplicate.
    expect(
      upsertSettingsProvider(original, { ...gateway, id: "my-gateway-2" })
        .written,
    ).toBe(true);
  });

  it("refuses a section it cannot extend safely (anchors, 4-space or tab indents)", () => {
    for (const original of [
      "providers: &shared\n  a:\n    base_url: https://a.example\n",
      "providers:\n    four-space:\n        base_url: https://a.example\n",
      "providers:\n\ttabbed:\n\t\tbase_url: https://a.example\n",
    ]) {
      const result = upsertSettingsProvider(original, gateway);
      expect(result.written, original).toBe(false);
      expect(result.text, original).toBe(original);
      expect(result.reason, original).toMatch(/by hand/);
    }
  });

  it("refuses an invalid definition instead of writing a block mecated would reject", () => {
    const bad = [
      { ...gateway, id: "openai" },
      { ...gateway, id: "Bad Id" },
      { ...gateway, apiFlavor: "grpc" },
      { ...gateway, baseURL: "http://plain.example" },
      { ...gateway, baseURL: "https://u:p@gw.example" },
      { ...gateway, defaultModel: "  " },
      { ...gateway, authMethod: "oidc" },
    ];
    for (const definition of bad) {
      const result = upsertSettingsProvider("x: 1\n", definition);
      expect(result.written, JSON.stringify(definition)).toBe(false);
      expect(result.text).toBe("x: 1\n");
      expect(result.reason).toBeTruthy();
    }
  });

  it("YAML-quotes the URL and model so ':' and spaces survive", () => {
    const { text } = upsertSettingsProvider("", {
      ...gateway,
      defaultModel: 'vendor/model: "quoted" v2',
    });
    expect(text).toContain(
      '    default_model: "vendor/model: \\"quoted\\" v2"',
    );
    expect(text).toContain('    base_url: "https://gw.example/v1"');
  });

  it("has no field a credential could travel through", () => {
    // A caller mistakenly passing a key must find it silently dropped: the
    // editor writes exactly its five known fields and nothing else.
    const smuggled = {
      ...gateway,
      apiKey: "sk-live-secret",
      api_key: "sk-live-secret",
    };
    const { text } = upsertSettingsProvider("", smuggled);
    expect(text).not.toMatch(/sk-live|api_key:/);
  });
});

describe("removeSettingsProvider", () => {
  it("round-trips: upsert then remove hands the original text back byte-identical", () => {
    for (const original of [
      "",
      "models:\n  default: x\n",
      "models:\n  default: x\npermissions:\n  allow: []",
      [
        "providers:",
        "  first-gw:",
        "    base_url: https://first.example",
        "    default_model: m",
        "    api_flavor: anthropic-messages",
        "",
        "permissions:",
        "  allow: []",
        "",
      ].join("\n"),
    ]) {
      const { text: added, written } = upsertSettingsProvider(
        original,
        gateway,
      );
      expect(written, original).toBe(true);
      const { text, removed } = removeSettingsProvider(added, gateway.id);
      expect(removed, original).toBe(true);
      // A file with no trailing newline gains one on the way in; that is
      // the one byte the round trip cannot give back.
      const expected =
        original === "" || original.endsWith("\n") ? original : `${original}\n`;
      expect(text, original).toBe(expected);
    }
  });

  it("removes exactly the named entry and keeps siblings, comments and the header", () => {
    const original = [
      "# operator settings",
      "providers:",
      "  a-gw:",
      '    base_url: "https://a.example"',
      "    default_model: a",
      "    api_flavor: openai-responses",
      "    auth:",
      "      method: none",
      "  # b is the keyed one",
      "  b-gw:",
      '    base_url: "https://b.example"',
      "    default_model: b",
      "    api_flavor: openai-responses",
      "permissions:",
      "  allow: []",
      "",
    ].join("\n");
    const { text, removed } = removeSettingsProvider(original, "a-gw");
    expect(removed).toBe(true);
    expect(text).toBe(
      [
        "# operator settings",
        "providers:",
        "  # b is the keyed one",
        "  b-gw:",
        '    base_url: "https://b.example"',
        "    default_model: b",
        "    api_flavor: openai-responses",
        "permissions:",
        "  allow: []",
        "",
      ].join("\n"),
    );
    // An operator's comment left at the entry indent keeps the header.
    const { text: onlyComment } = removeSettingsProvider(text, "b-gw");
    expect(onlyComment).toBe(
      [
        "# operator settings",
        "providers:",
        "  # b is the keyed one",
        "permissions:",
        "  allow: []",
        "",
      ].join("\n"),
    );
  });

  it("returns removed:false, text unchanged, for an absent id, a lookalike, or no section", () => {
    const original = customProviderSettingsSnippet(gateway);
    for (const missing of ["my-gateway-2", "my", "MY-GATEWAY", "nope"]) {
      expect(removeSettingsProvider(original, missing), missing).toEqual({
        text: original,
        removed: false,
      });
    }
    expect(
      removeSettingsProvider("models:\n  default: x\n", "my-gateway"),
    ).toEqual({
      text: "models:\n  default: x\n",
      removed: false,
    });
  });
});

describe("removeAuthFileProviderKey", () => {
  it("cuts only the api_key line, leaving the block and every other key intact", () => {
    const original = [
      "providers:",
      "  openrouter:",
      "    api_key: sk-or-live",
      "  my-gateway:",
      "    # rotated 2026-01",
      "    api_key: sk-gw",
      "    oauth:",
      "      access_token: tok",
      "  anthropic:",
      "    api_key: sk-ant",
      "other_top_level: true",
      "",
    ].join("\n");
    const { text, removed } = removeAuthFileProviderKey(original, "my-gateway");
    expect(removed).toBe(true);
    expect(text).toBe(
      [
        "providers:",
        "  openrouter:",
        "    api_key: sk-or-live",
        "  my-gateway:",
        "    # rotated 2026-01",
        "    oauth:",
        "      access_token: tok",
        "  anthropic:",
        "    api_key: sk-ant",
        "other_top_level: true",
        "",
      ].join("\n"),
    );
    expect(text).not.toContain("sk-gw");
    // The block is still there and lists as "configured, no key"... except
    // its oauth token still counts as a credential in this contrived shape.
    expect(listAuthFileProviders(text).map((p) => p.name)).toContain(
      "my-gateway",
    );
  });

  it("rewrites an emptied entry as `name: {}` — the daemon's own logout shape", () => {
    const original = [
      "providers:",
      "  my-gateway:",
      "    api_key: sk-gw",
      "  openai:",
      '    api_key: "sk-oai"',
      "",
    ].join("\n");
    const { text, removed } = removeAuthFileProviderKey(original, "my-gateway");
    expect(removed).toBe(true);
    expect(text).toBe(
      [
        "providers:",
        "  my-gateway: {}",
        "  openai:",
        '    api_key: "sk-oai"',
        "",
      ].join("\n"),
    );
    expect(listAuthFileProviders(text)).toEqual([
      { name: "my-gateway", keyPresent: false },
      { name: "openai", keyPresent: true },
    ]);
    // And the whole-block remover still finds the emptied entry afterwards.
    expect(removeAuthFileProvider(text, "my-gateway").text).toBe(
      ["providers:", "  openai:", '    api_key: "sk-oai"', ""].join("\n"),
    );
  });

  it("never matches an api_key line of another block, and reports no key as removed:false", () => {
    const original = [
      "providers:",
      "  a:",
      "    api_key: sk-a",
      "  b: {}",
      "  c:",
      "    oauth:",
      "      access_token: tok",
      "",
    ].join("\n");
    for (const name of ["b", "c", "nope", "A"]) {
      expect(removeAuthFileProviderKey(original, name), name).toEqual({
        text: original,
        removed: false,
      });
    }
    expect(removeAuthFileProviderKey("", "a")).toEqual({
      text: "",
      removed: false,
    });
  });
});
