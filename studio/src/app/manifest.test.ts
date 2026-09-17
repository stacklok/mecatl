import { describe, expect, it } from "vitest";
import manifest from "./manifest";

/**
 * The PWA share target lands shared content in the chat route as the
 * `?prompt=` deep link's PREFILL-ONLY form: a GET to `/workspace/chat` whose
 * text/title/url map onto the names `resolveChatSeed` reads, and never a
 * `send` parameter — a share pre-fills the composer, the user presses Enter.
 */
describe("manifest share_target", () => {
  it("shares into the chat route as a prefill-only seed", () => {
    const target = manifest().share_target;
    expect(target).toEqual({
      action: "/workspace/chat",
      method: "GET",
      params: { text: "prompt", title: "title", url: "url" },
    });
    expect(Object.values(target?.params ?? {})).not.toContain("send");
  });

  it("keeps the standalone app opening on the chat", () => {
    expect(manifest().start_url).toBe("/workspace/chat");
  });
});
