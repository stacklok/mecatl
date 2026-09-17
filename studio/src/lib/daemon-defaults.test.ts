import { describe, expect, it } from "vitest";
import {
  BASE_URL_KINDS,
  DAEMON_DEFAULTS_EMPTY,
  daemonDefaultArgs,
  normalizeDaemonDefaults,
  RESERVED_SLOTS,
  validDaemonBaseURL,
  validModelKey,
  validToolhiveBaseURL,
} from "./daemon-defaults.mjs";

/**
 * The controller's daemon-defaults grammar: what a saved document or PUT
 * /daemon-defaults body normalises to, which strangers it refuses, and —
 * the half that decides what mecated is actually started with — exactly
 * which flags a document becomes, per provider kind, with NOTHING emitted
 * for an empty document.
 */

const thrown = (
  fn: () => unknown,
): { message: string; statusCode?: number } => {
  try {
    fn();
  } catch (error) {
    return error as { message: string; statusCode?: number };
  }
  throw new Error("expected a throw");
};

const full = {
  models: {
    openrouter: {
      defaultModel: " anthropic/claude-sonnet-4 ",
      subagentModel: "openai/gpt-4o-mini",
    },
    anthropic: { defaultModel: "claude-sonnet-4-5" },
    opencode: { defaultModel: "", subagentModel: "" },
  },
  reasoningEffort: "High",
  contextWindowOverride: "200000",
  llmTimeouts: { perAttemptSeconds: " 600 ", streamIdleSeconds: 0 },
  promptCache: { disabled: true, anthropicTtl: "1h" },
  baseUrls: {
    openrouter: "https://gw.example/v1 ",
    openai: "http://127.0.0.1:4000/v1",
  },
  toolhive: {
    enabled: false,
    baseUrl: "http://localhost:14000/v1",
    mode: "proxy",
  },
  aliases: { smart: "openai/gpt-5", fast: "openai/gpt-4o-mini" },
  slots: { compaction: "fast", cheap: "openai/gpt-4o-mini" },
  apiKeyFile: "/home/op/.config/mecatl/team-auth.yaml",
  activeProvider: "OpenRouter",
};

describe("normalizeDaemonDefaults", () => {
  it("accepts exactly the closed vocabularies mecated knows", () => {
    for (const effort of ["", "low", "medium", "high", "xhigh", "max"]) {
      expect(
        normalizeDaemonDefaults({ reasoningEffort: effort }).reasoningEffort,
      ).toBe(effort);
    }
    for (const anthropicTtl of ["", "5m", "1h"]) {
      expect(
        normalizeDaemonDefaults({ promptCache: { anthropicTtl } }).promptCache
          .anthropicTtl,
      ).toBe(anthropicTtl);
    }
    for (const mode of ["auto", "proxy", "direct"]) {
      expect(
        normalizeDaemonDefaults({ toolhive: { mode } }).toolhive.mode,
      ).toBe(mode);
    }
    expect(BASE_URL_KINDS).toEqual([
      "openrouter",
      "openai",
      "anthropic",
      "opencode",
    ]);
    expect(RESERVED_SLOTS).toEqual(["router"]);
  });

  it("fills an empty or absent document with the flag-omitted values", () => {
    for (const input of [undefined, null, {}, [], "junk"]) {
      expect(normalizeDaemonDefaults(input)).toEqual(DAEMON_DEFAULTS_EMPTY);
    }
    expect(normalizeDaemonDefaults(DAEMON_DEFAULTS_EMPTY)).toEqual(
      DAEMON_DEFAULTS_EMPTY,
    );
  });

  it("trims, lower-cases enums, sorts maps and drops empty model entries", () => {
    expect(
      normalizeDaemonDefaults(full, { configDir: "/home/op/.config/mecatl" }),
    ).toEqual({
      models: {
        anthropic: { defaultModel: "claude-sonnet-4-5", subagentModel: "" },
        openrouter: {
          defaultModel: "anthropic/claude-sonnet-4",
          subagentModel: "openai/gpt-4o-mini",
        },
      },
      reasoningEffort: "high",
      contextWindowOverride: 200_000,
      llmTimeouts: { perAttemptSeconds: 600, streamIdleSeconds: 0 },
      promptCache: { disabled: true, anthropicTtl: "1h" },
      baseUrls: {
        openrouter: "https://gw.example/v1",
        openai: "http://127.0.0.1:4000/v1",
        anthropic: "",
        opencode: "",
      },
      toolhive: {
        enabled: false,
        baseUrl: "http://localhost:14000/v1",
        mode: "proxy",
      },
      aliases: { fast: "openai/gpt-4o-mini", smart: "openai/gpt-5" },
      slots: { cheap: "openai/gpt-4o-mini", compaction: "fast" },
      apiKeyFile: "/home/op/.config/mecatl/team-auth.yaml",
      activeProvider: "openrouter",
    });
  });

  it("reads auto as the unset effort and refuses a stranger", () => {
    expect(
      normalizeDaemonDefaults({ reasoningEffort: "auto" }).reasoningEffort,
    ).toBe("");
    expect(
      normalizeDaemonDefaults({ reasoningEffort: " MAX " }).reasoningEffort,
    ).toBe("max");
    const error = thrown(() =>
      normalizeDaemonDefaults({ reasoningEffort: "ultra" }),
    );
    expect(error.statusCode).toBe(400);
    expect(error.message).toMatch(/Unknown reasoning effort "ultra"/);
  });

  it("accepts a whole-number context window and refuses negatives, fractions and junk", () => {
    expect(
      normalizeDaemonDefaults({ contextWindowOverride: 0 })
        .contextWindowOverride,
    ).toBe(0);
    expect(
      normalizeDaemonDefaults({ contextWindowOverride: "" })
        .contextWindowOverride,
    ).toBe(0);
    expect(
      normalizeDaemonDefaults({ contextWindowOverride: 32000 })
        .contextWindowOverride,
    ).toBe(32_000);
    for (const bad of [-1, 1.5, "12k", "1e3", Number.NaN, 100_000_001, true]) {
      expect(
        thrown(() => normalizeDaemonDefaults({ contextWindowOverride: bad }))
          .message,
        String(bad),
      ).toMatch(/context window override must be a whole number/);
    }
  });

  it("reads an absent or blank LLM timeout as mecated's own default and 0 as disabled", () => {
    const defaults = { perAttemptSeconds: 300, streamIdleSeconds: 180 };
    expect(DAEMON_DEFAULTS_EMPTY.llmTimeouts).toEqual(defaults);
    expect(normalizeDaemonDefaults({}).llmTimeouts).toEqual(defaults);
    expect(normalizeDaemonDefaults({ llmTimeouts: {} }).llmTimeouts).toEqual(
      defaults,
    );
    expect(
      normalizeDaemonDefaults({
        llmTimeouts: { perAttemptSeconds: "", streamIdleSeconds: null },
      }).llmTimeouts,
    ).toEqual(defaults);
    // 0 is a real value — the bound disabled — never "unset".
    expect(
      normalizeDaemonDefaults({
        llmTimeouts: { perAttemptSeconds: 0, streamIdleSeconds: "0" },
      }).llmTimeouts,
    ).toEqual({ perAttemptSeconds: 0, streamIdleSeconds: 0 });
    expect(
      normalizeDaemonDefaults({
        llmTimeouts: { perAttemptSeconds: " 900 ", streamIdleSeconds: 86_400 },
      }).llmTimeouts,
    ).toEqual({ perAttemptSeconds: 900, streamIdleSeconds: 86_400 });
  });

  it("refuses a fractional, negative, unit-suffixed, oversized or unknown LLM timeout", () => {
    for (const bad of [
      -1,
      1.5,
      "300s",
      "5m",
      "1e3",
      Number.NaN,
      86_401,
      true,
    ]) {
      expect(
        thrown(() =>
          normalizeDaemonDefaults({ llmTimeouts: { perAttemptSeconds: bad } }),
        ).message,
        String(bad),
      ).toMatch(
        /LLM connect timeout must be a whole number of seconds between 0 and 86400/,
      );
      expect(
        thrown(() =>
          normalizeDaemonDefaults({ llmTimeouts: { streamIdleSeconds: bad } }),
        ).message,
        String(bad),
      ).toMatch(/LLM idle timeout must be a whole number of seconds/);
    }
    const stranger = thrown(() =>
      normalizeDaemonDefaults({ llmTimeouts: { perTurnSeconds: 5 } }),
    );
    expect(stranger.statusCode).toBe(400);
    expect(stranger.message).toMatch(
      /no LLM timeout flag for "perTurnSeconds"/,
    );
    expect(
      thrown(() => normalizeDaemonDefaults({ llmTimeouts: [300, 180] }))
        .message,
    ).toMatch(/llmTimeouts must be an object/);
  });

  it("refuses a cache TTL other than 5m or 1h", () => {
    expect(
      normalizeDaemonDefaults({ promptCache: { anthropicTtl: "5m" } })
        .promptCache,
    ).toEqual({ disabled: false, anthropicTtl: "5m" });
    expect(
      thrown(() =>
        normalizeDaemonDefaults({ promptCache: { anthropicTtl: "10m" } }),
      ).message,
    ).toMatch(/Unknown Anthropic cache TTL "10m"/);
  });

  it("refuses a base URL that is neither HTTPS nor loopback HTTP, and an unknown kind", () => {
    for (const bad of [
      "http://gateway.example/v1",
      "https://user:pw@gw.example/v1",
      "https://gw.example/v1?x=1",
      "https://gw.example/v1#frag",
      "ftp://gw.example",
      "not a url",
    ]) {
      expect(validDaemonBaseURL(bad), bad).toBe(false);
      expect(
        thrown(() => normalizeDaemonDefaults({ baseUrls: { anthropic: bad } }))
          .message,
      ).toMatch(/anthropic base URL must be an HTTPS URL/);
    }
    expect(validDaemonBaseURL("https://gw.example/v1")).toBe(true);
    expect(validDaemonBaseURL("http://localhost:4000/v1")).toBe(true);
    expect(validDaemonBaseURL("http://[::1]:4000/v1")).toBe(true);
    expect(
      thrown(() =>
        normalizeDaemonDefaults({
          baseUrls: { "openai-codex": "https://x.example" },
        }),
      ).message,
    ).toMatch(/no base-URL flag for "openai-codex"/);
  });

  it("requires the ToolHive proxy URL to be loopback and its mode to be known", () => {
    expect(validToolhiveBaseURL("http://127.0.0.1:14000/v1")).toBe(true);
    expect(validToolhiveBaseURL("https://localhost:14443/v1")).toBe(true);
    expect(validToolhiveBaseURL("https://gateway.example/v1")).toBe(false);
    expect(
      thrown(() =>
        normalizeDaemonDefaults({
          toolhive: { baseUrl: "https://gateway.example/v1" },
        }),
      ).message,
    ).toMatch(/ToolHive proxy URL must be an http\(s\) URL on loopback/);
    expect(
      thrown(() => normalizeDaemonDefaults({ toolhive: { mode: "tunnel" } }))
        .message,
    ).toMatch(/Unknown ToolHive routing mode "tunnel"/);
    expect(
      normalizeDaemonDefaults({ toolhive: { mode: "" } }).toolhive.mode,
    ).toBe("auto");
  });

  it("refuses to disable ToolHive detection while toolhive is the active provider", () => {
    expect(
      thrown(() =>
        normalizeDaemonDefaults({
          toolhive: { enabled: false },
          activeProvider: "toolhive",
        }),
      ).message,
    ).toMatch(/cannot be disabled while it is the active provider/);
    expect(
      normalizeDaemonDefaults({
        toolhive: { enabled: false },
        activeProvider: "mock",
      }).activeProvider,
    ).toBe("mock");
  });

  it("validates alias and slot keys, values, and the router-owned slot", () => {
    expect(validModelKey("fast")).toBe(true);
    expect(validModelKey("ask-reviewer")).toBe(true);
    for (const bad of ["", "Fast", "1st", "-x", "a b", "x".repeat(41), "a=b"]) {
      expect(validModelKey(bad), JSON.stringify(bad)).toBe(false);
      expect(
        thrown(() => normalizeDaemonDefaults({ aliases: { [bad]: "m" } }))
          .message,
      ).toMatch(/not a valid alias name/);
    }
    expect(
      thrown(() => normalizeDaemonDefaults({ slots: { router: "fast" } }))
        .message,
    ).toMatch(/"router" slot is reserved for model routing/);
    expect(
      thrown(() => normalizeDaemonDefaults({ aliases: { fast: "  " } }))
        .message,
    ).toMatch(/alias "fast" needs a model/);
    expect(
      thrown(() => normalizeDaemonDefaults({ slots: { cheap: "bad\nmodel" } }))
        .message,
    ).toMatch(/must not contain control characters/);
    expect(
      thrown(() => normalizeDaemonDefaults({ aliases: ["fast=x"] })).message,
    ).toMatch(/alias bindings must be an object/);
  });

  it("keys model defaults per provider and refuses the mock or a stranger", () => {
    expect(
      thrown(() =>
        normalizeDaemonDefaults({ models: { mock: { defaultModel: "x" } } }),
      ).message,
    ).toMatch(/"mock" is not a provider kind/);
    expect(
      thrown(() =>
        normalizeDaemonDefaults({
          models: { "bad/kind": { defaultModel: "x" } },
        }),
      ).message,
    ).toMatch(/not a provider kind/);
    expect(
      thrown(() => normalizeDaemonDefaults({ models: { openai: "gpt" } }))
        .message,
    ).toMatch(/models\.openai must be an object/);
    expect(
      thrown(() =>
        normalizeDaemonDefaults({
          models: { openai: { defaultModel: "x".repeat(201) } },
        }),
      ).message,
    ).toMatch(/longer than 200 characters/);
  });

  it("confines the credentials file to a .yaml inside the config directory", () => {
    const configDir = "/home/op/.config/mecatl";
    expect(
      normalizeDaemonDefaults(
        { apiKeyFile: " /home/op/.config/mecatl/work.yml " },
        { configDir },
      ).apiKeyFile,
    ).toBe("/home/op/.config/mecatl/work.yml");
    expect(
      normalizeDaemonDefaults({ apiKeyFile: "" }, { configDir }).apiKeyFile,
    ).toBe("");
    for (const [bad, pattern] of [
      ["relative/auth.yaml", /must be absolute/],
      ["/home/op/.config/mecatl/auth.json", /must be a \.yaml or \.yml file/],
      [
        "/home/op/.config/mecatl/../secrets/auth.yaml",
        /"\." or "\.\." segments/,
      ],
      ["/home/op/.config/mecatl//auth.yaml", /empty/],
      [
        "/etc/auth.yaml",
        /must live inside the daemon's mecatl config directory/,
      ],
      ["/home/op/.config/mecatl\0/auth.yaml", /NUL byte/],
    ] as const) {
      expect(
        thrown(() =>
          normalizeDaemonDefaults({ apiKeyFile: bad }, { configDir }),
        ).message,
        bad,
      ).toMatch(pattern);
    }
    // Without a config directory (the browser) the containment rule is the
    // controller's to enforce; the shape rules still apply.
    expect(
      normalizeDaemonDefaults({ apiKeyFile: "/etc/auth.yaml" }).apiKeyFile,
    ).toBe("/etc/auth.yaml");
  });

  it("normalises the active provider and refuses a malformed kind", () => {
    expect(
      normalizeDaemonDefaults({ activeProvider: null }).activeProvider,
    ).toBeNull();
    expect(
      normalizeDaemonDefaults({ activeProvider: "" }).activeProvider,
    ).toBeNull();
    expect(
      normalizeDaemonDefaults({ activeProvider: "Mock" }).activeProvider,
    ).toBe("mock");
    expect(
      normalizeDaemonDefaults({ activeProvider: "my-gateway" }).activeProvider,
    ).toBe("my-gateway");
    expect(
      thrown(() => normalizeDaemonDefaults({ activeProvider: "bad/kind" }))
        .message,
    ).toMatch(/not a valid provider kind/);
    expect(
      thrown(() => normalizeDaemonDefaults({ activeProvider: 7 })).message,
    ).toMatch(/activeProvider must be a provider kind or null/);
  });
});

describe("daemonDefaultArgs", () => {
  it("emits NOTHING for the empty document on every kind", () => {
    for (const kind of ["mock", "toolhive", "openrouter", ""]) {
      expect(daemonDefaultArgs(normalizeDaemonDefaults({}), kind)).toEqual([]);
    }
  });

  it("emits exactly the set flags, the model pair for the spawn kind only", () => {
    const defaults = normalizeDaemonDefaults(full, {
      configDir: "/home/op/.config/mecatl",
    });
    expect(daemonDefaultArgs(defaults, "openrouter")).toEqual([
      "--default-model",
      "anthropic/claude-sonnet-4",
      "--subagent-model",
      "openai/gpt-4o-mini",
      "--reasoning-effort",
      "high",
      "--context-window-override",
      "200000",
      "--llm-per-attempt-timeout",
      "600s",
      "--llm-stream-idle-timeout",
      "0s",
      "--no-prompt-cache",
      "--anthropic-cache-ttl",
      "1h",
      "--openrouter-base-url",
      "https://gw.example/v1",
      "--openai-base-url",
      "http://127.0.0.1:4000/v1",
      "--toolhive-llm=false",
      "--toolhive-llm-base-url",
      "http://localhost:14000/v1",
      "--toolhive-llm-mode",
      "proxy",
      "--model-alias",
      "fast=openai/gpt-4o-mini",
      "--model-alias",
      "smart=openai/gpt-5",
      "--model-slot",
      "cheap=openai/gpt-4o-mini",
      "--model-slot",
      "compaction=fast",
      "--api-key-file",
      "/home/op/.config/mecatl/team-auth.yaml",
    ]);
    // Anthropic has only a default model saved; openai has none; the mock
    // never receives a model pair (nothing to validate it against).
    expect(daemonDefaultArgs(defaults, "anthropic").slice(0, 3)).toEqual([
      "--default-model",
      "claude-sonnet-4-5",
      "--reasoning-effort",
    ]);
    expect(daemonDefaultArgs(defaults, "openai")[0]).toBe("--reasoning-effort");
    expect(daemonDefaultArgs(defaults, "mock")[0]).toBe("--reasoning-effort");
    expect(daemonDefaultArgs(defaults, "mock")).not.toContain(
      "--default-model",
    );
  });

  it("emits an LLM timeout as a Go duration only when it differs from mecated's default", () => {
    // mecated's own values, spelled out explicitly: still no flag.
    expect(
      daemonDefaultArgs(
        normalizeDaemonDefaults({
          llmTimeouts: { perAttemptSeconds: 300, streamIdleSeconds: 180 },
        }),
        "openai",
      ),
    ).toEqual([]);
    expect(
      daemonDefaultArgs(
        normalizeDaemonDefaults({ llmTimeouts: { perAttemptSeconds: 600 } }),
        "openai",
      ),
    ).toEqual(["--llm-per-attempt-timeout", "600s"]);
    // Disabling a bound is a real 0s flag, not an omission.
    expect(
      daemonDefaultArgs(
        normalizeDaemonDefaults({ llmTimeouts: { streamIdleSeconds: 0 } }),
        "mock",
      ),
    ).toEqual(["--llm-stream-idle-timeout", "0s"]);
  });

  it("passes --toolhive-llm=false only when detection is disabled and omits the auto mode", () => {
    const enabled = normalizeDaemonDefaults({
      toolhive: { enabled: true, mode: "auto" },
    });
    expect(daemonDefaultArgs(enabled, "mock")).toEqual([]);
    const direct = normalizeDaemonDefaults({ toolhive: { mode: "direct" } });
    expect(daemonDefaultArgs(direct, "toolhive")).toEqual([
      "--toolhive-llm-mode",
      "direct",
    ]);
    const off = normalizeDaemonDefaults({ toolhive: { enabled: false } });
    expect(daemonDefaultArgs(off, "mock")).toEqual(["--toolhive-llm=false"]);
  });

  it("keeps prompt caching on by default and can turn it off alone", () => {
    expect(
      daemonDefaultArgs(
        normalizeDaemonDefaults({ promptCache: { disabled: true } }),
        "openai",
      ),
    ).toEqual(["--no-prompt-cache"]);
  });
});
