// SPDX-License-Identifier: Apache-2.0

import type { GlobalSearchItem } from "./search-index";

/**
 * The palette's provider shape, ported from the prototype's
 * `features/search/search-types.ts`. Studio keeps its own entry type
 * (`GlobalSearchItem`) and never indexes a body, so a result carries no
 * snippet.
 */
export interface SearchResult {
  readonly entry: GlobalSearchItem;
}

/** Results for a query, already ranked. The palette depends only on this. */
export interface SearchProvider {
  query(q: string): SearchResult[];
}
