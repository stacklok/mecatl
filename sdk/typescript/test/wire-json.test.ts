import { fromJson } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  GetLearningProposalResponseSchema,
  GetSessionResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { ListSchedulesResponseSchema } from "../src/gen/mecatl/v1/schedule_pb.js";
import { normalizeWellKnownJson } from "../src/wire-json.js";

describe("normalizeWellKnownJson", () => {
  it("rewrites stdlib-JSON Timestamps nested in messages, lists, and oneofs", () => {
    const raw = {
      proposal: {
        created_at: { seconds: 1_700_000_000, nanos: 500_000_000 },
        decisions: [{ actor: "op", at: { seconds: 1_700_000_001 } }],
        id: "p1",
        updated_at: {},
      },
    };
    const normalized = normalizeWellKnownJson(GetLearningProposalResponseSchema, raw);
    expect(normalized).toEqual({
      proposal: {
        created_at: "2023-11-14T22:13:20.500Z",
        decisions: [{ actor: "op", at: "2023-11-14T22:13:21Z" }],
        id: "p1",
        updated_at: "1970-01-01T00:00:00Z",
      },
    });
    // The received value is never mutated.
    expect(raw.proposal.created_at).toEqual({ seconds: 1_700_000_000, nanos: 500_000_000 });
    const message = fromJson(GetLearningProposalResponseSchema, normalized, {
      ignoreUnknownFields: true,
    });
    expect(message.proposal?.createdAt?.seconds).toBe(1_700_000_000n);
    expect(message.proposal?.createdAt?.nanos).toBe(500_000_000);
    expect(message.proposal?.decisions[0]?.at?.seconds).toBe(1_700_000_001n);
  });

  it("rewrites Durations and one-shot Timestamps in schedule specs", () => {
    const normalized = normalizeWellKnownJson(ListSchedulesResponseSchema, {
      schedules: [
        {
          spec: {
            fire_timeout: { seconds: 90, nanos: 250_000_000 },
            name: "nightly",
            trigger: { one_shot: { seconds: "1700000000" } },
          },
          state: { next_fire_at: { seconds: 1_700_000_100, nanos: 0 } },
        },
      ],
    });
    expect(normalized).toEqual({
      schedules: [
        {
          spec: {
            fire_timeout: "90.250s",
            name: "nightly",
            trigger: { one_shot: "2023-11-14T22:13:20Z" },
          },
          state: { next_fire_at: "2023-11-14T22:15:00Z" },
        },
      ],
    });
    const message = fromJson(ListSchedulesResponseSchema, normalized, {
      ignoreUnknownFields: true,
    });
    expect(message.schedules[0]?.spec?.fireTimeout?.seconds).toBe(90n);
    expect(message.schedules[0]?.spec?.fireTimeout?.nanos).toBe(250_000_000);
  });

  it("returns the same value when nothing needs rewriting", () => {
    const raw = {
      session: { session_id: "s1", state: "idle", updated_at: "2023-11-14T22:13:20Z" },
    };
    expect(normalizeWellKnownJson(GetSessionResponseSchema, raw)).toBe(raw);
  });

  it("leaves a Timestamp-shaped object with foreign keys alone", () => {
    const raw = { session: { session_id: "s1", updated_at: { seconds: 1, other: true } } };
    expect(normalizeWellKnownJson(GetSessionResponseSchema, raw)).toBe(raw);
  });
});
