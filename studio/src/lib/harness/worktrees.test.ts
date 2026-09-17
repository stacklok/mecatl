import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "./sdk-test-stub";
import { clearHarnessSession } from "./sessions";
import {
  fetchHarnessWorktrees,
  isStaleWorktreeSelector,
  shortRevision,
  switchHarnessSessionWorktree,
} from "./worktrees";

/**
 * Pins the worktree picker's daemon calls as the SDK spells them: the list
 * is `GET /v1/worktrees?session_id=` decoded in daemon order, a clear or a
 * fork carries the opaque selector as `worktree_selector` and nothing
 * path-shaped (ADR 0291), and a stale selector is a typed refusal the UI
 * narrows on to re-list.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const WIRE_WORKTREES = [
  {
    selector: "wt-main",
    kind: "git-worktree",
    label: "main",
    branch: "main",
    revision: "abcdef0123456789",
    bare: false,
  },
  {
    selector: "wt-feature",
    kind: "git-worktree",
    label: "feature-x",
    branch: "feature/x",
    revision: "0123456789abcdef",
    bare: false,
  },
  {
    selector: "wt-bare",
    kind: "git-worktree",
    label: "archive",
    branch: "",
    revision: "",
    bare: true,
  },
];

describe("fetchHarnessWorktrees", () => {
  it("GETs /v1/worktrees keyed by the source session and keeps daemon order", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path.startsWith("/v1/worktrees"))
        return jsonResponse(200, { worktrees: WIRE_WORKTREES });
      return undefined;
    });
    const worktrees = await fetchHarnessWorktrees("s1");
    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("GET");
    const url = new URL(requests[0].url, "http://studio");
    expect(url.pathname).toBe("/api/mecatl/v1/worktrees");
    expect(url.searchParams.get("session_id")).toBe("s1");
    // No body on a GET; the placement query is the session id only.
    expect(requests[0].body).toBeUndefined();
    expect(worktrees).toEqual([
      {
        selector: "wt-main",
        kind: "git-worktree",
        label: "main",
        branch: "main",
        revision: "abcdef0123456789",
        bare: false,
      },
      {
        selector: "wt-feature",
        kind: "git-worktree",
        label: "feature-x",
        branch: "feature/x",
        revision: "0123456789abcdef",
        bare: false,
      },
      {
        selector: "wt-bare",
        kind: "git-worktree",
        label: "archive",
        branch: "",
        revision: "",
        bare: true,
      },
    ]);
  });

  it("returns an empty list for a session with no eligible worktrees (no-FS)", async () => {
    stubHarnessFetch((request) => {
      if (request.path.startsWith("/v1/worktrees"))
        return jsonResponse(200, { worktrees: [] });
      return undefined;
    });
    await expect(fetchHarnessWorktrees("s1")).resolves.toEqual([]);
  });

  it("drops a row the daemon sends without a selector", async () => {
    stubHarnessFetch((request) => {
      if (request.path.startsWith("/v1/worktrees"))
        return jsonResponse(200, {
          worktrees: [{ label: "orphan", branch: "x" }, WIRE_WORKTREES[0]],
        });
      return undefined;
    });
    const worktrees = await fetchHarnessWorktrees("s1");
    expect(worktrees.map((w) => w.selector)).toEqual(["wt-main"]);
  });

  it("surfaces a daemon refusal as HarnessApiError with its code", async () => {
    stubHarnessFetch((request) => {
      if (request.path.startsWith("/v1/worktrees"))
        return problemResponse(403, "management_unauthorized", "not the owner");
      return undefined;
    });
    await expect(fetchHarnessWorktrees("s1")).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 403,
      code: "management_unauthorized",
    });
  });
});

describe("isStaleWorktreeSelector", () => {
  it.each([
    "placement_selector_stale",
    "placement_selector_not_found",
    "placement_selector_invalid",
  ])("is true for %s", (code) => {
    expect(isStaleWorktreeSelector(new HarnessApiError(400, code, "x"))).toBe(
      true,
    );
  });

  it("is false for every other failure", () => {
    expect(
      isStaleWorktreeSelector(
        new HarnessApiError(412, "failed_precondition", "busy"),
      ),
    ).toBe(false);
    expect(
      isStaleWorktreeSelector(
        new HarnessApiError(400, "placement_unavailable", "x"),
      ),
    ).toBe(false);
    expect(isStaleWorktreeSelector(new Error("placement_selector_stale"))).toBe(
      false,
    );
    expect(isStaleWorktreeSelector(undefined)).toBe(false);
  });
});

describe("shortRevision", () => {
  it("abbreviates to seven characters and leaves a short or empty one alone", () => {
    expect(shortRevision("abcdef0123456789")).toBe("abcdef0");
    expect(shortRevision("abc")).toBe("abc");
    expect(shortRevision("")).toBe("");
  });
});

const snapshotFor = (sessionId: string) =>
  jsonResponse(200, sessionSnapshot(sessionId));

describe("clearHarnessSession with a worktree selector", () => {
  it("POSTs the clear route with worktree_selector and nothing else", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return jsonResponse(200, { session_id: "s2" });
      return undefined;
    });
    await expect(
      clearHarnessSession("s1", { worktreeSelector: "wt-feature" }),
    ).resolves.toBe("s2");
    const clear = requests.find((r) => r.path === "/v1/sessions/s1/clear");
    expect(clear?.method).toBe("POST");
    expect(clear?.body).toEqual({ worktree_selector: "wt-feature" });
  });

  it("omits worktree_selector when no worktree is picked", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return jsonResponse(200, { session_id: "s2" });
      return undefined;
    });
    await clearHarnessSession("s1", { worktreeSelector: "" });
    const clear = requests.find((r) => r.path === "/v1/sessions/s1/clear");
    expect(clear?.body ?? {}).toEqual({});
  });

  it("a stale selector rejects with a typed code the picker narrows on", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return problemResponse(
          400,
          "placement_selector_stale",
          "selector expired; list worktrees again",
        );
      return undefined;
    });
    const failure = await clearHarnessSession("s1", {
      worktreeSelector: "wt-old",
    }).catch((caught: unknown) => caught);
    expect(failure).toBeInstanceOf(HarnessApiError);
    expect(isStaleWorktreeSelector(failure)).toBe(true);
  });
});

describe("switchHarnessSessionWorktree", () => {
  it("clear mode mints the successor via the clear route with the selector", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return jsonResponse(200, { session_id: "s2" });
      return undefined;
    });
    await expect(
      switchHarnessSessionWorktree("s1", "wt-feature", "clear", "Title"),
    ).resolves.toBe("s2");
    const clear = requests.find((r) => r.path === "/v1/sessions/s1/clear");
    expect(clear?.body).toEqual({ worktree_selector: "wt-feature" });
    expect(requests.some((r) => r.path.endsWith("/fork"))).toBe(false);
  });

  it("fork mode carries the conversation via the fork route with title + selector", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1/fork")
        return jsonResponse(200, { session_id: "s3" });
      return undefined;
    });
    await expect(
      switchHarnessSessionWorktree("s1", "wt-feature", "fork", "Title"),
    ).resolves.toBe("s3");
    const fork = requests.find((r) => r.path === "/v1/sessions/s1/fork");
    expect(fork?.method).toBe("POST");
    expect(fork?.body).toEqual({
      title: "Title",
      worktree_selector: "wt-feature",
    });
  });
});
