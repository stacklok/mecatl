// SPDX-License-Identifier: Apache-2.0

import type {
  CreateScheduleRequest,
  ListScheduleFiresResponse,
  ListSchedulesResponse,
  ScheduleResponse,
  UpdateScheduleRequest,
} from "@mecatl-studio/contracts";
import type { Client } from "@stacklok-oss/mecatl-sdk";
import {
  PermissionMode,
  type Schedule,
  type ScheduleFire,
  type ScheduleSpec,
} from "@stacklok-oss/mecatl-sdk/gen";

/**
 * The daemon keeps a schedule's stored next fire when its spec is updated, so
 * a changed trigger would be saved but never take effect: a one-shot moved to
 * a later day still fires at the old instant, and a finished one-shot stays
 * finished. Updates therefore refuse any trigger change.
 */
export class ScheduleTriggerImmutableError extends Error {
  constructor(name: string) {
    super(
      `The trigger of schedule "${name}" cannot be changed. To change when it runs, create a new schedule and delete this one.`,
    );
    this.name = "ScheduleTriggerImmutableError";
  }
}

export interface ScheduleService {
  readonly supported: boolean;
  create(request: CreateScheduleRequest): Promise<ScheduleResponse>;
  delete(name: string): Promise<void>;
  fire(name: string): Promise<void>;
  list(): Promise<ListSchedulesResponse>;
  listFires(name: string): Promise<ListScheduleFiresResponse>;
  pause(name: string): Promise<void>;
  resume(name: string): Promise<void>;
  update(name: string, request: UpdateScheduleRequest): Promise<ScheduleResponse>;
}

export function createMecatlScheduleService(
  client: Client,
  supported: boolean | (() => boolean),
): ScheduleService {
  const isSupported = typeof supported === "function" ? supported : () => supported;

  return {
    get supported() {
      return isSupported();
    },

    async create(request) {
      const response = await client.schedules.create({
        $typeName: "mecatl.v1.CreateScheduleRequest",
        spec: toSdkSpec(request.name, request),
      });
      return scheduleFromSdk(response.schedule);
    },

    async delete(name) {
      await client.schedules.delete({ $typeName: "mecatl.v1.DeleteScheduleRequest", name });
    },

    async fire(name) {
      await client.schedules.fireNow({ $typeName: "mecatl.v1.FireNowRequest", name });
    },

    async list() {
      if (!isSupported()) {
        return {
          items: [],
          reason: "Scheduling is not enabled on this Mecatl deployment.",
          supported: false,
        };
      }
      const response = await client.schedules.list({
        $typeName: "mecatl.v1.ListSchedulesRequest",
      });
      return {
        items: response.schedules
          .map(scheduleFromSdk)
          .sort((left, right) => left.name.localeCompare(right.name)),
        reason: "",
        supported: true,
      };
    },

    async listFires(name) {
      const response = await client.schedules.listFires({
        $typeName: "mecatl.v1.ListFiresRequest",
        scheduleName: name,
      });
      return {
        items: response.fires
          .flatMap((fire) => (fire.id ? [fireFromSdk(fire)] : []))
          .sort((left, right) => (right.firedAt ?? "").localeCompare(left.firedAt ?? "")),
      };
    },

    async pause(name) {
      await client.schedules.pause({ $typeName: "mecatl.v1.PauseScheduleRequest", name });
    },

    async resume(name) {
      await client.schedules.resume({ $typeName: "mecatl.v1.ResumeScheduleRequest", name });
    },

    async update(name, request) {
      const current = await client.schedules.get({
        $typeName: "mecatl.v1.GetScheduleRequest",
        name,
      });
      const stored = current.schedule?.spec;
      if (stored && !sameTrigger(request.trigger, stored)) {
        throw new ScheduleTriggerImmutableError(name);
      }
      const response = await client.schedules.update({
        $typeName: "mecatl.v1.UpdateScheduleRequest",
        spec: toSdkSpec(name, request, stored),
      });
      return scheduleFromSdk(response.schedule);
    },
  };
}

function toSdkSpec(
  name: string,
  request: UpdateScheduleRequest,
  current?: ScheduleSpec,
): ScheduleSpec {
  const cron = request.trigger.kind === "cron" ? request.trigger : undefined;
  const once = request.trigger.kind === "once" ? request.trigger : undefined;

  return {
    $typeName: "mecatl.v1.ScheduleSpec",
    carryContext: current?.carryContext ?? false,
    createdAt: undefined,
    fireTimeout: current?.fireTimeout,
    limits: current?.limits ?? {
      $typeName: "mecatl.v1.Limits",
      maxConsecutiveFailures: 0,
      maxToolCalls: 0,
      maxTurns: 0,
    },
    maxFires: cron ? request.maxFires : 0,
    misfire: current?.misfire ?? 0,
    mode: modeToSdk(request.mode),
    mutating: request.mutating,
    name,
    oneShotMaxRetries: once && request.oneShotRetry ? request.oneShotMaxRetries : 0,
    oneShotRetry: Boolean(once && request.oneShotRetry),
    owner: undefined,
    parts: current?.parts ?? [],
    profile: profileToSdk(request.profile),
    prompt: request.prompt,
    selector: current?.selector,
    singleton: current?.singleton ?? false,
    timezone: cron?.timezone ?? "",
    trigger: {
      $typeName: "mecatl.v1.TriggerSpec",
      cron: cron?.expression ?? "",
      oneShot: once ? isoToTimestamp(once.at) : undefined,
    },
  };
}

/** Whether a requested trigger names the same firing rule as the stored spec. */
function sameTrigger(requested: UpdateScheduleRequest["trigger"], stored: ScheduleSpec): boolean {
  const cron = stored.trigger?.cron ?? "";
  if (requested.kind === "cron") {
    return cron !== "" && requested.expression === cron && requested.timezone === stored.timezone;
  }
  if (cron !== "") return false;
  const storedAt = timestampToIso(stored.trigger?.oneShot);
  // Compare instants, not strings: the same moment may be written with a
  // numeric offset or in UTC.
  return storedAt !== null && new Date(requested.at).getTime() === new Date(storedAt).getTime();
}

function scheduleFromSdk(schedule: Schedule | undefined): ScheduleResponse {
  const spec = schedule?.spec;
  const state = schedule?.state;
  if (!spec?.name || !spec.trigger) {
    throw new Error("Mecatl returned an incomplete schedule");
  }

  const lastFireSessionId = state?.lastFireSessionId ?? "";
  const running = timestampToIso(state?.lastFireStartedAt) !== null;
  const nextFireAt = timestampToIso(state?.nextFireAt);
  const fireCount = state?.fireCount ?? 0;
  // A one-shot that has run, or a cron that reached maxFires, has nothing
  // left to do: the daemon's claim clears the next fire and disables it in
  // the same write. That is "completed", not "paused" — resuming it would
  // schedule nothing. An in-flight last fire still reads as running.
  const finished = nextFireAt === null && fireCount > 0;
  const status = running
    ? "running"
    : lastFireSessionId === "pending"
      ? "claimed"
      : finished
        ? "completed"
        : !state?.enabled
          ? "paused"
          : "scheduled";

  return {
    enabled: state?.enabled ?? false,
    fireCount,
    lastFireAt: timestampToIso(state?.lastFireAt),
    lastFireSessionId: lastFireSessionId === "pending" ? "" : lastFireSessionId,
    maxFires: spec.maxFires,
    mode: modeFromSdk(spec.mode),
    modelId: spec.selector?.modelId ?? "",
    mutating: spec.mutating,
    name: spec.name,
    nextFireAt,
    oneShotMaxRetries: spec.oneShotMaxRetries,
    oneShotRetry: spec.oneShotRetry,
    owner: spec.owner?.name || spec.owner?.subject || "",
    profile: profileFromSdk(spec.profile),
    prompt: spec.prompt,
    providerId: spec.selector?.providerId ?? "",
    status,
    trigger: spec.trigger.cron
      ? { expression: spec.trigger.cron, kind: "cron", timezone: spec.timezone }
      : { at: requiredTimestamp(spec.trigger.oneShot), kind: "once" },
  };
}

function fireFromSdk(fire: ScheduleFire) {
  return {
    deadline: timestampToIso(fire.deadline),
    error: fire.err,
    firedAt: timestampToIso(fire.firedAt),
    id: fire.id,
    inFlight: !fire.stop,
    progressAt: timestampToIso(fire.progressAt),
    scheduleName: fire.scheduleName,
    sessionId: fire.sessionId,
    startedAt: timestampToIso(fire.startedAt),
    stop: fire.stop,
  };
}

function modeToSdk(mode: UpdateScheduleRequest["mode"]): PermissionMode {
  if (mode === "default") return PermissionMode.DEFAULT;
  if (mode === "acceptEdits") return PermissionMode.ACCEPT_EDITS;
  return PermissionMode.PLAN;
}

function modeFromSdk(mode: PermissionMode): ScheduleResponse["mode"] {
  if (mode === PermissionMode.DEFAULT) return "default";
  if (mode === PermissionMode.ACCEPT_EDITS) return "acceptEdits";
  return "plan";
}

function profileToSdk(profile: UpdateScheduleRequest["profile"]): string {
  return profile === "noFilesystem" ? "no-fs" : "";
}

function profileFromSdk(profile: string): ScheduleResponse["profile"] {
  return profile === "no-fs" ? "noFilesystem" : "all";
}

function isoToTimestamp(value: string) {
  const milliseconds = new Date(value).getTime();
  const seconds = Math.floor(milliseconds / 1_000);
  return {
    $typeName: "google.protobuf.Timestamp" as const,
    nanos: (milliseconds - seconds * 1_000) * 1_000_000,
    seconds: BigInt(seconds),
  };
}

function requiredTimestamp(value: Parameters<typeof timestampToIso>[0]): string {
  const result = timestampToIso(value);
  if (result === null) throw new Error("Mecatl returned a one-shot schedule without a time");
  return result;
}

function timestampToIso(value: { nanos: number; seconds: bigint } | undefined): string | null {
  if (!value || value.seconds <= 0n) return null;
  return new Date(
    Number(value.seconds) * 1_000 + Math.floor(value.nanos / 1_000_000),
  ).toISOString();
}
