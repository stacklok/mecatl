import type { ComponentType } from "react";

/**
 * Shared shapes for the global search. Each domain (admin fixtures, Atrium
 * workspace) owns its own typed registry and tags its entries with a `source`;
 * the palette merges them at the edge. Filtering goes through a `SearchProvider`
 * so the static in-memory index can later be swapped for a server-backed one
 * without touching the UI.
 */

type SearchSource = "admin" | "atrium";

export interface SearchEntry {
  /** Stable key for React keys and cmdk values. */
  readonly id: string;
  readonly source: SearchSource;
  /** Grouping key within the entry's source (e.g. "chat", "connector"). */
  readonly category: string;
  /** Primary line shown in the result row. */
  readonly title: string;
  /** Muted secondary line (e.g. project name, email, host). */
  readonly subtitle: string;
  /** Absolute route this result navigates to. */
  readonly href: string;
  /** Leading icon for the row. */
  readonly icon: ComponentType<{ className?: string }>;
  /** Extra free text to match on but not display. */
  readonly keywords?: readonly string[];
  /**
   * Long-form body (e.g. a chat transcript) — matched, and a snippet around the
   * match is shown when the hit lands here rather than in the title.
   */
  readonly body?: string;
}

export interface SearchGroup {
  readonly category: string;
  readonly heading: string;
  readonly source: SearchSource;
}

export interface SearchResult {
  readonly entry: SearchEntry;
  /** A short excerpt around the match when it's in the body, not the title. */
  readonly snippet?: string;
}

export interface SearchProvider {
  /** Results for a query (all entries when the query is empty). */
  query(q: string): SearchResult[];
}
