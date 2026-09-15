import type { SearchEntry, SearchProvider, SearchResult } from "./search-types";

/**
 * A static, in-memory search provider. Each entry is flattened once into a
 * lowercased haystack; a query matches when every whitespace-separated token is
 * a substring of the haystack (so multi-word queries narrow rather than fuzz).
 * When the match lands in an entry's `body` (not its title), a short snippet
 * around the first token is returned for display.
 *
 * The same interface can be backed by a server search later — the palette only
 * depends on `SearchProvider`.
 */
export function createStaticSearchProvider(
  entries: readonly SearchEntry[],
): SearchProvider {
  const indexed = entries.map((entry) => ({
    entry,
    haystack: [
      entry.title,
      entry.subtitle,
      ...(entry.keywords ?? []),
      entry.body ?? "",
    ]
      .join(" ")
      .toLowerCase(),
  }));

  return {
    query(q: string): SearchResult[] {
      const tokens = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
      if (tokens.length === 0) {
        return entries.map((entry) => ({ entry }));
      }
      const results: SearchResult[] = [];
      for (const { entry, haystack } of indexed) {
        if (!tokens.every((t) => haystack.includes(t))) continue;
        const titleHasMatch = tokens.some((t) =>
          entry.title.toLowerCase().includes(t),
        );
        const snippet =
          !titleHasMatch && entry.body
            ? snippetFor(entry.body, tokens)
            : undefined;
        results.push({ entry, snippet });
      }
      return results;
    },
  };
}

/** A trimmed excerpt of `body` around the first matching token. */
function snippetFor(body: string, tokens: string[]): string | undefined {
  const lower = body.toLowerCase();
  let idx = -1;
  for (const t of tokens) {
    const at = lower.indexOf(t);
    if (at !== -1 && (idx === -1 || at < idx)) idx = at;
  }
  if (idx === -1) return undefined;
  const start = Math.max(0, idx - 32);
  const end = Math.min(body.length, idx + 48);
  let excerpt = body.slice(start, end).replace(/\s+/g, " ").trim();
  if (start > 0) excerpt = `…${excerpt}`;
  if (end < body.length) excerpt = `${excerpt}…`;
  return excerpt;
}
