import { describe, expect, it } from "vitest";

import type { Event, WatchEnvelope } from "../src/index.js";
import { PlanApprovalRequiredError } from "../src/index.js";
import { connect, query } from "../src/node.js";
import { fixture, type ReadyDocument, withDaemon } from "./harness.js";

function endpoint(ready: ReadyDocument): string {
  return `http://${ready.grpc_address}`;
}

function resultRunIds(envelopes: readonly WatchEnvelope[]): string[] {
  return envelopes.flatMap((envelope) =>
    envelope.kind === "event" && envelope.event.kind === "result" ? [envelope.event.runId] : [],
  );
}

describe("offline plan-resolution wire", () => {
  it("attachments remain bound to one run during plan resolution", async () => {
    await withDaemon({ durable: true, script: fixture("plan-request.json") }, async (daemon) => {
      const planningClient = connect({ baseUrl: endpoint(daemon.ready) });
      let resolvingClient: ReturnType<typeof connect> | undefined;
      try {
        const planningSession = await planningClient.sessions.create({ mode: 2 });
        const parkedRun = await planningSession.run("prepare a plan");
        const parkedEvents = parkedRun[Symbol.asyncIterator]();
        for (;;) {
          const next = await parkedEvents.next();
          if (next.done) throw new Error("plan run ended before its approval ask");
          if (next.value.kind === "permission.ask") {
            expect(next.value.payload.tool).toBe("PresentPlan");
            break;
          }
        }

        await daemon.restart({ script: fixture("plan-continuation.json") });
        await planningClient.close();
        resolvingClient = connect({ baseUrl: endpoint(daemon.ready) });
        const session = await resolvingClient.sessions.get(planningSession.id);
        const attachment = await session.attach(parkedRun.id);
        const activity = await session.activity();
        const attachedEnvelopes: WatchEnvelope[] = [];
        const activityEnvelopes: WatchEnvelope[] = [];
        const attachedDone = (async () => {
          for await (const envelope of attachment) attachedEnvelopes.push(envelope);
        })();
        const activityDone = (async () => {
          for await (const envelope of activity) {
            activityEnvelopes.push(envelope);
            if (resultRunIds(activityEnvelopes).length === 2) break;
          }
        })();

        const outcome = await session.resolvePlan().result();
        await Promise.all([attachedDone, activityDone]);

        expect(outcome.resumed.runId).toBe(parkedRun.id);
        expect(outcome.resumed.stopReason).toBe("plan_approved");
        expect(outcome.continuation).toMatchObject({
          stopReason: "end_turn",
          text: "executed the approved plan",
        });
        expect(outcome.continuation?.runId).not.toBe(parkedRun.id);
        expect(resultRunIds(attachedEnvelopes)).toEqual([parkedRun.id]);
        expect(resultRunIds(activityEnvelopes)).toEqual([
          parkedRun.id,
          outcome.continuation?.runId,
        ]);
        await session.delete();
      } finally {
        await resolvingClient?.close();
        await planningClient.close();
      }
    });
  });

  it("query plan mode requires onPlanApproval and flattens both runs", async () => {
    await withDaemon({ script: fixture("plan-query.json") }, async ({ ready }) => {
      const client = connect({ baseUrl: endpoint(ready) });
      try {
        await expect(
          query("must refuse before create", { client, session: { mode: 2 } }),
        ).rejects.toBeInstanceOf(PlanApprovalRequiredError);

        const planAsks: string[] = [];
        const permissionAsks: string[] = [];
        const oneShot = await query("prepare then execute", {
          client,
          onPermissionAsk: (ask) => {
            permissionAsks.push(ask.askId);
            return "deny";
          },
          onPlanApproval: (ask) => {
            planAsks.push(ask.askId);
            return "approve";
          },
          session: { mode: 2 },
        });
        const events: Event[] = [];
        for await (const event of oneShot) events.push(event);

        const results = events.filter((event) => event.kind === "result");
        expect(results.map((event) => event.payload.stop)).toEqual(["plan_approved", "end_turn"]);
        expect(results[0]?.runId).not.toBe(results[1]?.runId);
        expect(results[1]?.payload.text).toBe("query executed the approved plan");
        expect(planAsks).toHaveLength(1);
        expect(permissionAsks).toEqual([]);
      } finally {
        await client.close();
      }
    });
  });
});
