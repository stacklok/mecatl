import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";
import { fetchAllSessions, type SessionWalkProgress } from "./sessions";

/**
 * The paged inventory walk behind the chat sidebar: each page is reported as
 * it lands (cumulative counts plus that page's rows, so the UI can merge and
 * show them before the walk ends), the page bound reports `complete: false`
 * rather than looping, and an aborted signal rejects the walk mid-way while
 * the pages already reported stand.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const row = (id: string) => ({
  session_id: id,
  title: `Chat ${id}`,
  capabilities: { rename: true, delete: true, reasons: {} },
});

const cursorOf = (url: string) =>
  new URL(url, "http://studio.test").searchParams.get("cursor") ?? "";

describe("fetchAllSessions", () => {
  it("reports each page as it lands with cumulative counts and that page's rows", async () => {
    const pages: Record<string, unknown> = {
      "": { sessions: [row("s1"), row("s2")], next_cursor: "c2" },
      c2: { sessions: [row("s3")], next_cursor: "" },
    };
    const stub = stubHarnessFetch((request) => pages[cursorOf(request.url)]);
    const seen: SessionWalkProgress[] = [];

    const walk = await fetchAllSessions(undefined, 25, (progress) =>
      seen.push(progress),
    );

    expect(stub.requests.map((request) => cursorOf(request.url))).toEqual([
      "",
      "c2",
    ]);
    expect(seen.map(({ pages, rows }) => ({ pages, rows }))).toEqual([
      { pages: 1, rows: 2 },
      { pages: 2, rows: 3 },
    ]);
    expect(seen[0].page.map((s) => s.sessionId)).toEqual(["s1", "s2"]);
    expect(seen[1].page.map((s) => s.sessionId)).toEqual(["s3"]);
    expect(walk.complete).toBe(true);
    expect(walk.sessions.map((s) => s.sessionId)).toEqual(["s1", "s2", "s3"]);
  });

  it("stops at the page bound and reports the walk incomplete", async () => {
    let served = 0;
    const stub = stubHarnessFetch(() => {
      served += 1;
      return { sessions: [row(`s${served}`)], next_cursor: `c${served + 1}` };
    });
    const onProgress = vi.fn();

    const walk = await fetchAllSessions(undefined, 2, onProgress);

    expect(stub.requests).toHaveLength(2);
    expect(onProgress).toHaveBeenCalledTimes(2);
    expect(onProgress).toHaveBeenLastCalledWith(
      expect.objectContaining({ pages: 2, rows: 2 }),
    );
    expect(walk.complete).toBe(false);
    expect(walk.sessions.map((s) => s.sessionId)).toEqual(["s1", "s2"]);
  });

  it("rejects when the signal aborts mid-walk, keeping only the pages already reported", async () => {
    const controller = new AbortController();
    stubHarnessFetch((request) => {
      // A real fetch rejects an aborted request; the stub mirrors that.
      if (request.init?.signal?.aborted) {
        throw new DOMException("The operation was aborted.", "AbortError");
      }
      // The first page lands, then the user stops the walk.
      controller.abort();
      return jsonResponse(200, {
        sessions: [row("s1")],
        next_cursor: "c2",
      });
    });
    const onProgress = vi.fn();

    await expect(
      fetchAllSessions(controller.signal, 25, onProgress),
    ).rejects.toBeDefined();

    expect(onProgress).toHaveBeenCalledTimes(1);
    expect(onProgress).toHaveBeenCalledWith(
      expect.objectContaining({ pages: 1, rows: 1 }),
    );
  });

  it("walks without a progress callback exactly as before", async () => {
    stubHarnessFetch(() => ({ sessions: [row("s1")], next_cursor: "" }));
    await expect(fetchAllSessions()).resolves.toMatchObject({
      complete: true,
    });
  });
});
