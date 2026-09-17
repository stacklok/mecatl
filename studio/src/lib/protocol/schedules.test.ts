import type {
  Content,
  ListFiresResponse,
  ListSchedulesResponse,
  Schedule,
  ScheduleFire,
  ScheduleSpec,
  ScheduleState,
} from "@stacklok-oss/mecatl-sdk/gen";
import { describe, expect, it } from "vitest";
import {
  decodeScheduleFires,
  decodeScheduleRows,
  encodeScheduleSpec,
  type ScheduleSpecDraft,
  scheduleDraftFromRow,
} from "./schedules";

// ── Message fixtures ────────────────────────────────────────────────────────
// The decoders take the SDK's generated messages, so the fixtures are complete
// message values (every proto3 field at its zero value unless overridden).

type Timestamp = NonNullable<ScheduleSpec["createdAt"]>;

function ts(seconds: number, nanos = 0): Timestamp {
  return {
    $typeName: "google.protobuf.Timestamp",
    seconds: BigInt(seconds),
    nanos,
  };
}

function spec(overrides: Partial<ScheduleSpec> = {}): ScheduleSpec {
  return {
    $typeName: "mecatl.v1.ScheduleSpec",
    name: "",
    prompt: "",
    parts: [],
    trigger: undefined,
    selector: undefined,
    profile: "",
    mode: 0,
    limits: undefined,
    mutating: false,
    maxFires: 0,
    misfire: 0,
    singleton: false,
    timezone: "",
    createdAt: undefined,
    oneShotRetry: false,
    oneShotMaxRetries: 0,
    carryContext: false,
    fireTimeout: undefined,
    owner: undefined,
    ...overrides,
  };
}

function state(overrides: Partial<ScheduleState> = {}): ScheduleState {
  return {
    $typeName: "mecatl.v1.ScheduleState",
    nextFireAt: undefined,
    lastFireAt: undefined,
    fireCount: 0,
    enabled: false,
    lastFireSessionId: "",
    oneShotRetryCount: 0,
    lastFireStartedAt: undefined,
    lastFireProgressAt: undefined,
    fireDeadline: undefined,
    ...overrides,
  };
}

function schedule(
  specOverrides: Partial<ScheduleSpec>,
  stateOverrides: Partial<ScheduleState> = {},
): Schedule {
  return {
    $typeName: "mecatl.v1.Schedule",
    spec: spec(specOverrides),
    state: state(stateOverrides),
  };
}

function listResponse(...schedules: Schedule[]): ListSchedulesResponse {
  return { $typeName: "mecatl.v1.ListSchedulesResponse", schedules };
}

function fire(overrides: Partial<ScheduleFire>): ScheduleFire {
  return {
    $typeName: "mecatl.v1.ScheduleFire",
    id: "",
    scheduleName: "",
    sessionId: "",
    firedAt: undefined,
    stop: "",
    err: "",
    startedAt: undefined,
    progressAt: undefined,
    deadline: undefined,
    ...overrides,
  };
}

function firesResponse(...fires: ScheduleFire[]): ListFiresResponse {
  return { $typeName: "mecatl.v1.ListFiresResponse", fires };
}

const imagePart: Content = {
  $typeName: "mecatl.v1.Content",
  kind: 2,
  mimeType: "image/png",
  data: new Uint8Array([104, 105]),
  url: "",
};

const nightly = schedule(
  {
    name: "nightly-digest",
    prompt: "summarise the day",
    trigger: { $typeName: "mecatl.v1.TriggerSpec", cron: "0 9 * * *" },
    timezone: "Europe/London",
    mode: 2,
    maxFires: 30,
    limits: {
      $typeName: "mecatl.v1.Limits",
      maxTurns: 8,
      maxToolCalls: 40,
      maxConsecutiveFailures: 3,
    },
    selector: {
      $typeName: "mecatl.v1.ScheduleProviderSelector",
      providerId: "openrouter",
      modelId: "big-1",
    },
    misfire: 2,
    singleton: true,
    carryContext: true,
    fireTimeout: {
      $typeName: "google.protobuf.Duration",
      seconds: BigInt(900),
      nanos: 0,
    },
    parts: [imagePart],
    owner: {
      $typeName: "mecatl.v1.Principal",
      issuer: "https://idp",
      subject: "sub-1",
      grantType: "user",
      name: "James",
    },
  },
  {
    enabled: true,
    fireCount: 4,
    nextFireAt: ts(1700003600),
    lastFireAt: ts(1700000000, 500000000),
    lastFireSessionId: "sched--nightly-digest-20260817-090000-abcdef",
  },
);

const baseDraft: ScheduleSpecDraft = {
  name: "n",
  prompt: "p",
  trigger: { kind: "cron", cron: "* * * * *", timezone: "" },
  profile: "",
  mode: 2,
  mutating: false,
  maxFires: 0,
  limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
  oneShotRetry: false,
  oneShotMaxRetries: 0,
};

describe("decodeScheduleRows", () => {
  it("maps spec + state messages onto a display row", () => {
    const [row] = decodeScheduleRows(listResponse(nightly));
    expect(row).toMatchObject({
      name: "nightly-digest",
      cron: "0 9 * * *",
      oneShotAt: null,
      timezone: "Europe/London",
      mode: 2,
      mutating: false,
      maxFires: 30,
      enabled: true,
      fireCount: 4,
      owner: "James",
      lastFireSessionId: "sched--nightly-digest-20260817-090000-abcdef",
      fireStage: "idle",
    });
    expect(row.nextFireAt).toBe(1700003600000);
    expect(row.lastFireAt).toBe(1700000000500);
    expect(row.limits).toEqual({
      maxTurns: 8,
      maxToolCalls: 40,
      maxConsecutiveFailures: 3,
    });
  });

  it("reads a one-shot trigger and a fractional fire timeout", () => {
    const [row] = decodeScheduleRows(
      listResponse(
        schedule({
          name: "x",
          trigger: {
            $typeName: "mecatl.v1.TriggerSpec",
            cron: "",
            oneShot: ts(Date.parse("2026-08-20T09:00:00Z") / 1000),
          },
          fireTimeout: {
            $typeName: "google.protobuf.Duration",
            seconds: BigInt(1),
            nanos: 500000000,
          },
        }),
      ),
    );
    expect(row.cron).toBe("");
    expect(row.oneShotAt).toBe(Date.parse("2026-08-20T09:00:00Z"));
    expect(row.carried.fireTimeoutSeconds).toBe(1.5);
    // The zero Timestamp is "not set", never 1970.
    expect(row.nextFireAt).toBeNull();
  });

  it("falls back to the owner's subject when the display name is empty", () => {
    const [row] = decodeScheduleRows(
      listResponse(
        schedule({
          name: "x",
          owner: {
            $typeName: "mecatl.v1.Principal",
            issuer: "",
            subject: "sub-1",
            grantType: "",
            name: "",
          },
        }),
      ),
    );
    expect(row.owner).toBe("sub-1");
  });

  it("derives the fire stage from the claim sentinel and the started timestamp", () => {
    const [claimed] = decodeScheduleRows(
      listResponse(schedule({ name: "a" }, { lastFireSessionId: "pending" })),
    );
    expect(claimed.fireStage).toBe("claimed");
    // The pending sentinel is a claim, not a session id.
    expect(claimed.lastFireSessionId).toBe("");

    const [running] = decodeScheduleRows(
      listResponse(
        schedule({ name: "a" }, { lastFireStartedAt: ts(1700000000) }),
      ),
    );
    expect(running.fireStage).toBe("running");
  });

  it("tolerates an entry with no spec or state", () => {
    const [row] = decodeScheduleRows(
      listResponse({ $typeName: "mecatl.v1.Schedule" }),
    );
    expect(row.name).toBe("");
    expect(row.fireStage).toBe("idle");
    expect(row.carried.parts).toEqual([]);
  });

  it("carries the fields the form cannot edit for the update round trip", () => {
    const [row] = decodeScheduleRows(listResponse(nightly));
    expect(row.carried).toEqual({
      selectorProvider: "openrouter",
      selectorModel: "big-1",
      misfire: 2,
      singleton: true,
      carryContext: true,
      fireTimeoutSeconds: 900,
      parts: [imagePart],
    });
  });
});

describe("scheduleDraftFromRow", () => {
  it("splits a one-shot row into a one-shot trigger draft", () => {
    const [row] = decodeScheduleRows(
      listResponse(
        schedule({
          name: "x",
          trigger: {
            $typeName: "mecatl.v1.TriggerSpec",
            cron: "",
            oneShot: ts(1700000000),
          },
          profile: "no-fs",
        }),
      ),
    );
    expect(scheduleDraftFromRow(row)).toMatchObject({
      trigger: { kind: "one-shot", at: 1700000000000 },
      profile: "no-fs",
    });
  });
});

describe("encodeScheduleSpec", () => {
  it("re-encodes a decoded row as a spec message: carried fields survive", () => {
    const [row] = decodeScheduleRows(listResponse(nightly));
    const encoded = encodeScheduleSpec(scheduleDraftFromRow(row), row.carried);
    expect(encoded.$typeName).toBe("mecatl.v1.ScheduleSpec");
    expect(encoded.fireTimeout).toEqual({
      $typeName: "google.protobuf.Duration",
      seconds: BigInt(900),
      nanos: 0,
    });
    expect(encoded.selector).toMatchObject({
      providerId: "openrouter",
      modelId: "big-1",
    });
    expect(encoded.singleton).toBe(true);
    expect(encoded.misfire).toBe(2);
    expect(encoded.carryContext).toBe(true);
    expect(encoded.parts).toEqual([imagePart]);
    expect(encoded.limits).toMatchObject({
      maxTurns: 8,
      maxToolCalls: 40,
      maxConsecutiveFailures: 3,
    });
    // Server-owned fields are never set.
    expect(encoded.owner).toBeUndefined();
    expect(encoded.createdAt).toBeUndefined();
  });

  it("sends trigger-conditional fields only on the trigger they belong to", () => {
    const cron = encodeScheduleSpec({
      ...baseDraft,
      name: "c",
      trigger: { kind: "cron", cron: "* * * * *", timezone: "UTC" },
      maxFires: 10,
      oneShotRetry: true,
      oneShotMaxRetries: 3,
    });
    expect(cron.trigger).toMatchObject({ cron: "* * * * *" });
    expect(cron.trigger?.oneShot).toBeUndefined();
    expect(cron.timezone).toBe("UTC");
    expect(cron.maxFires).toBe(10);
    // A cron carrying one_shot_retry is REJECTED by the create seam.
    expect(cron.oneShotRetry).toBe(false);
    expect(cron.oneShotMaxRetries).toBe(0);

    const at = Date.parse("2026-08-20T09:00:00.250Z");
    const oneShot = encodeScheduleSpec({
      ...baseDraft,
      name: "o",
      trigger: { kind: "one-shot", at },
      maxFires: 10,
      oneShotRetry: true,
      oneShotMaxRetries: 3,
    });
    expect(oneShot.trigger?.cron).toBe("");
    expect(oneShot.trigger?.oneShot).toEqual(
      ts(Math.floor(at / 1000), 250000000),
    );
    expect(oneShot.oneShotRetry).toBe(true);
    expect(oneShot.oneShotMaxRetries).toBe(3);
    // max_fires and timezone belong to cron only.
    expect(oneShot.maxFires).toBe(0);
    expect(oneShot.timezone).toBe("");
  });

  it("sends no retry cap when one-shot retry is off", () => {
    const encoded = encodeScheduleSpec({
      ...baseDraft,
      trigger: { kind: "one-shot", at: 1700000000000 },
      oneShotRetry: false,
      oneShotMaxRetries: 3,
    });
    expect(encoded.oneShotRetry).toBe(false);
    expect(encoded.oneShotMaxRetries).toBe(0);
  });

  it("leaves carried fields at their zero value on a create, where there is nothing to preserve", () => {
    const encoded = encodeScheduleSpec(baseDraft);
    expect(encoded.singleton).toBe(false);
    expect(encoded.misfire).toBe(0);
    expect(encoded.selector).toBeUndefined();
    expect(encoded.fireTimeout).toBeUndefined();
    expect(encoded.parts).toEqual([]);
  });

  it("omits the selector when the carried spec has neither provider nor model", () => {
    const encoded = encodeScheduleSpec(baseDraft, {
      selectorProvider: "",
      selectorModel: "",
      misfire: 1,
      singleton: true,
      carryContext: false,
      fireTimeoutSeconds: 0,
      parts: [],
    });
    expect(encoded.selector).toBeUndefined();
    expect(encoded.fireTimeout).toBeUndefined();
    expect(encoded.misfire).toBe(1);
  });
});

describe("decodeScheduleFires", () => {
  it("orders newest first, keys in-flight off the absent stop, and drops idless records", () => {
    const fires = decodeScheduleFires(
      firesResponse(
        fire({
          id: "f-old",
          sessionId: "s-old",
          firedAt: ts(1700000000),
          stop: "end_turn",
        }),
        fire({
          id: "f-new",
          sessionId: "s-new",
          firedAt: ts(1700007200),
          startedAt: ts(1700007201),
        }),
        fire({ sessionId: "corrupt-no-id" }),
      ),
    );
    expect(fires.map((entry) => entry.id)).toEqual(["f-new", "f-old"]);
    expect(fires[0].inFlight).toBe(true);
    expect(fires[0].startedAt).toBe(1700007201000);
    expect(fires[1].inFlight).toBe(false);
    expect(fires[1].stop).toBe("end_turn");
  });
});
