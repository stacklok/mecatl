// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { providerDisplayName } from "./provider-names";

describe("provider display names", () => {
  it("names each provider the daemon registers", () => {
    expect(
      Object.fromEntries(
        [
          "anthropic",
          "mock",
          "openai",
          "openai-codex",
          "opencode",
          "openrouter",
          "openrouter-anthropic",
          "toolhive",
          "toolhive-anthropic",
        ].map((id) => [id, providerDisplayName(id)]),
      ),
    ).toEqual({
      anthropic: "Anthropic",
      mock: "Mock",
      openai: "OpenAI",
      "openai-codex": "OpenAI Codex",
      opencode: "OpenCode Go",
      openrouter: "OpenRouter",
      "openrouter-anthropic": "OpenRouter Anthropic",
      toolhive: "ToolHive",
      "toolhive-anthropic": "ToolHive Anthropic",
    });
  });

  it("title-cases any other ID word by word, as Studio named every provider before", () => {
    expect(providerDisplayName("team-gateway")).toBe("Team Gateway");
    expect(providerDisplayName("acme-llm")).toBe("Acme Llm");
    expect(providerDisplayName("local_llm")).toBe("Local Llm");
    expect(providerDisplayName("a--b__c")).toBe("A B C");
    expect(providerDisplayName("team/openai")).toBe("Team/openai");
    expect(providerDisplayName("gpu2")).toBe("Gpu2");
    expect(providerDisplayName("")).toBe("");
  });

  it("matches a known ID exactly, never by case or a prefix", () => {
    expect(providerDisplayName("OpenAI")).toBe("OpenAI");
    expect(providerDisplayName("OPENAI")).toBe("OPENAI");
    expect(providerDisplayName("openai-gateway")).toBe("Openai Gateway");
    expect(providerDisplayName("my-openai")).toBe("My Openai");
  });

  it("treats object built-in names as ordinary IDs", () => {
    expect(providerDisplayName("constructor")).toBe("Constructor");
    expect(providerDisplayName("toString")).toBe("ToString");
    expect(providerDisplayName("__proto__")).toBe("Proto");
    expect(providerDisplayName("has-own-property")).toBe("Has Own Property");
  });
});
