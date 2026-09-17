import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "./sdk-test-stub";
import { cancelHarnessChild } from "./sessions";

/**
 * Pins the per-child cancel contract as the SDK spells it: ONE child of the
 * session's live run (a subagent, a parallel branch, or a team member) is
 * cancelled by `POST /v1/sessions/{id}/cancel-child` with `{ child_id }`,
 * the run itself keeps streaming. Unlike the whole-run cancel this is not
 * fire-and-forget: the UI reports the outcome, so a finished child reads as
 * `not_found` and a refusal propagates typed.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const snapshotFor = (sessionId: string) =>
  jsonResponse(200, sessionSnapshot(sessionId));

describe("cancelHarnessChild", () => {
  it("POSTs { child_id } to the cancel-child route after the snapshot GET and reads a 204 as cancelled", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel-child") {
        return new Response(null, { status: 204 });
      }
      return undefined;
    });
    await expect(cancelHarnessChild("s1", "subagent-abc")).resolves.toBe(
      "cancelled",
    );
    expect(requests.map((r) => r.path)).toEqual([
      "/v1/sessions/s1",
      "/v1/sessions/s1/cancel-child",
    ]);
    const cancel = requests[1];
    expect(cancel.method).toBe("POST");
    expect(cancel.body).toEqual({ child_id: "subagent-abc" });
  });

  it("reads an empty 200 body as cancelled too (the e2e fixture's shape)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel-child") return {};
      return undefined;
    });
    await expect(cancelHarnessChild("s1", "parallel-2")).resolves.toBe(
      "cancelled",
    );
  });

  it("returns not_found for a 404 child_not_found problem — the child already finished", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel-child") {
        return problemResponse(404, "child_not_found", "no such child");
      }
      return undefined;
    });
    await expect(cancelHarnessChild("s1", "subagent-gone")).resolves.toBe(
      "not_found",
    );
  });

  it("rethrows a 409 no_active_run as the typed error the UI reports", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel-child") {
        return problemResponse(409, "no_active_run", "session has no run");
      }
      return undefined;
    });
    await expect(
      cancelHarnessChild("s1", "subagent-abc"),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 409,
      code: "no_active_run",
    });
  });

  it("refuses an empty child id before touching the wire", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      return undefined;
    });
    await expect(cancelHarnessChild("s1", "")).rejects.toBeInstanceOf(Error);
    expect(
      requests.filter((r) => r.path === "/v1/sessions/s1/cancel-child"),
    ).toHaveLength(0);
  });
});
