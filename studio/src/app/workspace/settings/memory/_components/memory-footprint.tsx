import type { MemoryStoreFootprint } from "@/features/agent";

/**
 * "3 facts remembered" — the one-line summary above the facts table. Only
 * the count is shown; the store's byte size and digest are reported by the
 * agent but mean nothing to the person reading the page, so they stay off it.
 * Empty when there is nothing to count.
 */
export function formatMemoryFootprint(store: MemoryStoreFootprint): string {
  if (store.count <= 0) return "";
  return `${store.count} ${store.count === 1 ? "fact" : "facts"} remembered`;
}

/** The summary line above the facts table; renders nothing when empty. */
export function MemoryFootprint({ store }: { store: MemoryStoreFootprint }) {
  const text = formatMemoryFootprint(store);
  if (!text) return null;
  return (
    <p data-testid="memory-footprint" className="text-xs text-muted-foreground">
      {text}
    </p>
  );
}
