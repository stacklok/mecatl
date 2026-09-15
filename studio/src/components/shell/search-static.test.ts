import { Server } from "lucide-react";
import { describe, expect, it } from "vitest";
import { createStaticSearchProvider } from "./search-static";
import type { SearchEntry } from "./search-types";

function entry(
  partial: Partial<SearchEntry> & Pick<SearchEntry, "id">,
): SearchEntry {
  return {
    source: "atrium",
    category: "chat",
    title: "Untitled",
    subtitle: "",
    href: `/x/${partial.id}`,
    icon: Server,
    ...partial,
  };
}

const ENTRIES: SearchEntry[] = [
  entry({
    id: "a",
    title: "Auth service",
    subtitle: "Platform",
    keywords: ["login", "oauth"],
  }),
  entry({
    id: "b",
    title: "Billing chat",
    subtitle: "Finance",
    body: "We debated whether to sign the jsonwebtoken payload before caching it downstream.",
  }),
  entry({ id: "c", title: "Random note", subtitle: "Misc" }),
];

describe("createStaticSearchProvider", () => {
  const provider = createStaticSearchProvider(ENTRIES);

  it("returns every entry for an empty query", () => {
    expect(provider.query("")).toHaveLength(ENTRIES.length);
    expect(provider.query("   ")).toHaveLength(ENTRIES.length);
  });

  it("matches on title, subtitle and keywords", () => {
    expect(provider.query("auth").map((r) => r.entry.id)).toEqual(["a"]);
    expect(provider.query("oauth").map((r) => r.entry.id)).toEqual(["a"]);
    expect(provider.query("finance").map((r) => r.entry.id)).toEqual(["b"]);
  });

  it("narrows with multi-token AND rather than fuzzing", () => {
    // Both tokens present in entry a's haystack.
    expect(provider.query("auth login").map((r) => r.entry.id)).toEqual(["a"]);
    // "auth" matches a, "finance" matches b — no single entry has both.
    expect(provider.query("auth finance")).toHaveLength(0);
  });

  it("finds terms inside a body and returns a snippet for body-only hits", () => {
    const hits = provider.query("jsonwebtoken");
    expect(hits.map((r) => r.entry.id)).toEqual(["b"]);
    expect(hits[0].snippet).toContain("jsonwebtoken");
    expect(hits[0].snippet).toMatch(/…/);
  });

  it("does not attach a snippet when the match is in the title", () => {
    const hits = provider.query("billing");
    expect(hits.map((r) => r.entry.id)).toEqual(["b"]);
    expect(hits[0].snippet).toBeUndefined();
  });

  it("is case-insensitive", () => {
    expect(provider.query("AUTH").map((r) => r.entry.id)).toEqual(["a"]);
  });
});
