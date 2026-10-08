// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";
import { sessionToolAccessSchema } from "./chat.ts";

/**
 * The daemon's fire counters are signed 32-bit protobuf fields, so a larger
 * value cannot be represented: reject it here rather than let serialisation
 * coerce or fail on it.
 */
export const maximumInt32 = 2_147_483_647;

export const scheduleModeSchema = z.enum(["default", "plan", "acceptEdits"]);

/** Each segment of an IANA zone name starts with an uppercase letter. */
const ianaZoneName = /^[A-Z][A-Za-z0-9_+-]*(?:\/[A-Z][A-Za-z0-9_+-]*)*$/u;

/**
 * Whether `value` names an IANA time zone the daemon can load. The daemon
 * silently falls back to UTC for a name it cannot load, so a typo would run
 * the schedule in the wrong zone; the empty string is the explicit UTC choice
 * and is accepted.
 *
 * `Intl.supportedValuesOf("timeZone")` lists only canonical zones (it omits
 * `UTC` and aliases such as `US/Eastern`, which the daemon accepts), so the
 * check asks `Intl.DateTimeFormat` instead. That constructor also accepts
 * numeric offsets and case-folded names the daemon's zone database rejects;
 * the name pattern and the case check exclude both.
 */
export function isScheduleTimeZone(value: string): boolean {
  if (value === "") return true;
  if (!ianaZoneName.test(value)) return false;
  let resolved: string;
  try {
    resolved = new Intl.DateTimeFormat("en-US", { timeZone: value }).resolvedOptions().timeZone;
  } catch {
    return false;
  }
  return resolved === value || resolved.toLowerCase() !== value.toLowerCase();
}

export const scheduleTriggerSchema = z.discriminatedUnion("kind", [
  z.object({
    expression: z.string().trim().min(1),
    kind: z.literal("cron"),
    timezone: z
      .string()
      .trim()
      .refine(isScheduleTimeZone, {
        error: (issue) =>
          `Unknown time zone ${JSON.stringify(issue.input)}; use an IANA name such as Europe/Rome, or leave it empty for UTC`,
      }),
  }),
  z.object({
    at: z.iso.datetime({ offset: true }),
    kind: z.literal("once"),
  }),
]);

export const scheduleWriteSchema = z.object({
  maxFires: z.number().int().nonnegative().max(maximumInt32).default(0),
  mode: scheduleModeSchema.default("plan"),
  mutating: z.boolean().default(false),
  oneShotMaxRetries: z.number().int().nonnegative().max(maximumInt32).default(0),
  oneShotRetry: z.boolean().default(false),
  profile: sessionToolAccessSchema.default("all"),
  prompt: z.string().trim().min(1).max(1_000_000),
  trigger: scheduleTriggerSchema,
});

export const createScheduleRequestSchema = scheduleWriteSchema.extend({
  name: z.string().trim().min(1).max(160),
});

export const updateScheduleRequestSchema = scheduleWriteSchema;

export const scheduleSchema = z.object({
  enabled: z.boolean(),
  fireCount: z.number().int().nonnegative(),
  lastFireAt: z.string().nullable(),
  lastFireSessionId: z.string(),
  maxFires: z.number().int().nonnegative(),
  mode: scheduleModeSchema,
  modelId: z.string(),
  mutating: z.boolean(),
  name: z.string(),
  nextFireAt: z.string().nullable(),
  oneShotMaxRetries: z.number().int().nonnegative(),
  oneShotRetry: z.boolean(),
  owner: z.string(),
  profile: sessionToolAccessSchema,
  prompt: z.string(),
  providerId: z.string(),
  status: z.enum(["claimed", "completed", "paused", "running", "scheduled"]),
  trigger: scheduleTriggerSchema,
});

export const listSchedulesResponseSchema = z.object({
  items: z.array(scheduleSchema),
  reason: z.string(),
  supported: z.boolean(),
});

export const scheduleFireSchema = z.object({
  deadline: z.string().nullable(),
  error: z.string(),
  firedAt: z.string().nullable(),
  id: z.string(),
  inFlight: z.boolean(),
  progressAt: z.string().nullable(),
  scheduleName: z.string(),
  sessionId: z.string(),
  startedAt: z.string().nullable(),
  stop: z.string(),
});

export const listScheduleFiresResponseSchema = z.object({
  items: z.array(scheduleFireSchema),
});

export const scheduleActionRequestSchema = z.object({
  action: z.enum(["fire", "pause", "resume"]),
});

export type CreateScheduleRequest = z.infer<typeof createScheduleRequestSchema>;
export type ListScheduleFiresResponse = z.infer<typeof listScheduleFiresResponseSchema>;
export type ListSchedulesResponse = z.infer<typeof listSchedulesResponseSchema>;
export type ScheduleResponse = z.infer<typeof scheduleSchema>;
export type UpdateScheduleRequest = z.infer<typeof updateScheduleRequestSchema>;
