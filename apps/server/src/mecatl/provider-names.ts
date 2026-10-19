// SPDX-License-Identifier: Apache-2.0

/**
 * The display names of the provider IDs the daemon registers. The daemon and
 * its SDK identify a provider only by ID, so Studio keeps this one map and the
 * browser shows the name the BFF sends. The IDs follow the daemon's provider
 * registry (`internal/app/registry.go`, listed in
 * `docs/architecture/providers.md`).
 */
const providerDisplayNames: ReadonlyMap<string, string> = new Map([
  ["anthropic", "Anthropic"],
  ["mock", "Mock"],
  ["openai", "OpenAI"],
  ["openai-codex", "OpenAI Codex"],
  ["opencode", "OpenCode Go"],
  ["openrouter", "OpenRouter"],
  ["openrouter-anthropic", "OpenRouter Anthropic"],
  ["toolhive", "ToolHive"],
  ["toolhive-anthropic", "ToolHive Anthropic"],
]);

/**
 * The name Studio shows for a provider. An ID outside the map, such as an
 * operator-defined provider, is title-cased word by word, which is how Studio
 * named every provider before this map.
 */
export function providerDisplayName(providerId: string): string {
  return providerDisplayNames.get(providerId) ?? titleCaseId(providerId);
}

function titleCaseId(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
