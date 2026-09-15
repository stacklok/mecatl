/**
 * The daemon's schedule registry: rows, fire history, create/replace, and the
 * pause / resume / fire / delete actions — all through `client.schedules`.
 */

import {
  decodeScheduleFires,
  decodeScheduleRows,
  encodeScheduleSpec,
  type ScheduleCarriedSpec,
  type ScheduleFireRow,
  type ScheduleRow,
  type ScheduleSpecDraft,
} from "@/lib/protocol";
import { getHarnessClient, harness } from "./sdk";

/**
 * Runs one schedule action. NOTE: "fire" is synchronous on the harness — the
 * request stays open for the whole agent run, which can be minutes.
 */
export async function harnessScheduleAction(
  name: string,
  action: "pause" | "resume" | "fire" | "delete",
): Promise<void> {
  const client = getHarnessClient();
  switch (action) {
    case "pause":
      await harness(() =>
        client.schedules.pause({
          $typeName: "mecatl.v1.PauseScheduleRequest",
          name,
        }),
      );
      return;
    case "resume":
      await harness(() =>
        client.schedules.resume({
          $typeName: "mecatl.v1.ResumeScheduleRequest",
          name,
        }),
      );
      return;
    case "fire":
      await harness(() =>
        client.schedules.fireNow({
          $typeName: "mecatl.v1.FireNowRequest",
          name,
        }),
      );
      return;
    case "delete":
      await harness(() =>
        client.schedules.delete({
          $typeName: "mecatl.v1.DeleteScheduleRequest",
          name,
        }),
      );
      return;
  }
}

/**
 * Reads the schedule registry as display rows: spec fields (mode, mutating,
 * timezone, limits), state, fire stage, and the carried spec an edit must
 * round-trip. A daemon without a schedule store answers the list with 501,
 * which surfaces as a `HarnessApiError` with that status — the distinct
 * "scheduler not wired" state, never conflated with an empty registry.
 */
export async function listScheduleRows(
  signal?: AbortSignal,
): Promise<ScheduleRow[]> {
  const client = getHarnessClient();
  const response = await harness(() =>
    client.schedules.list(
      { $typeName: "mecatl.v1.ListSchedulesRequest" },
      { signal },
    ),
  );
  return decodeScheduleRows(response);
}

/**
 * Creates or replaces a schedule spec. The body is built by
 * encodeScheduleSpec, never by echoing a decoded response — and an update must
 * pass the row's `carried` spec or the fields this UI cannot edit would be
 * silently deleted (`update` REPLACES the whole spec).
 */
export async function saveHarnessSchedule(
  draft: ScheduleSpecDraft,
  options: { update: boolean; carried?: ScheduleCarriedSpec },
): Promise<void> {
  const client = getHarnessClient();
  const spec = encodeScheduleSpec(draft, options.carried);
  if (options.update) {
    await harness(() =>
      client.schedules.update({
        $typeName: "mecatl.v1.UpdateScheduleRequest",
        spec,
      }),
    );
    return;
  }
  await harness(() =>
    client.schedules.create({
      $typeName: "mecatl.v1.CreateScheduleRequest",
      spec,
    }),
  );
}

/** Reads a schedule's fire history, newest first. */
export async function listScheduleFires(
  name: string,
  signal?: AbortSignal,
): Promise<ScheduleFireRow[]> {
  const client = getHarnessClient();
  const response = await harness(() =>
    client.schedules.listFires(
      { $typeName: "mecatl.v1.ListFiresRequest", scheduleName: name },
      { signal },
    ),
  );
  return decodeScheduleFires(response);
}
