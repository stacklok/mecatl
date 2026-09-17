import {
  create,
  createFileRegistry,
  type DescMessage,
  type JsonValue,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  DurationSchema,
  FileDescriptorProtoSchema,
  FieldDescriptorProto_Label as Label,
  TimestampSchema,
  FieldDescriptorProto_Type as Type,
} from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "../src/gen/mecatl/v1/schedule_pb.js";
import { normalizeHttpWktJson } from "../src/http-wkt.js";
import { createHttpTransport, getRawJson, ProtocolError } from "../src/index.js";

function httpJson(body: JsonValue): ReturnType<typeof createHttpTransport> {
  const fetch: typeof globalThis.fetch = async () => Response.json(body);
  return createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
}

async function unarySchedule(body: JsonValue) {
  const response = await httpJson(body).unary(
    ScheduleService.method.getSchedule,
    undefined,
    undefined,
    undefined,
    { name: "nightly" },
  );
  return response.message;
}

async function unarySchedules(body: JsonValue) {
  const response = await httpJson(body).unary(
    ScheduleService.method.listSchedules,
    undefined,
    undefined,
    undefined,
    {},
  );
  return response.message;
}

function sseTransport(frames: JsonValue[]): ReturnType<typeof createHttpTransport> {
  const encoded = frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join("");
  const fetch: typeof globalThis.fetch = async () =>
    new Response(encoded, { headers: { "content-type": "text/event-stream" } });
  return createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
}

async function* one<T>(value: T): AsyncIterable<T> {
  yield value;
}

function descriptorFixture(): DescMessage {
  const descriptor = create(FileDescriptorProtoSchema, {
    dependency: ["google/protobuf/timestamp.proto", "google/protobuf/duration.proto"],
    messageType: [
      {
        field: [
          {
            jsonName: "createdAt",
            label: Label.OPTIONAL,
            name: "created_at",
            number: 1,
            type: Type.MESSAGE,
            typeName: ".google.protobuf.Timestamp",
          },
          {
            jsonName: "children",
            label: Label.REPEATED,
            name: "children",
            number: 2,
            type: Type.MESSAGE,
            typeName: ".httpwkt.Child",
          },
          {
            jsonName: "deadlines",
            label: Label.REPEATED,
            name: "deadlines",
            number: 3,
            type: Type.MESSAGE,
            typeName: ".httpwkt.Envelope.DeadlinesEntry",
          },
          {
            jsonName: "ordinary",
            label: Label.OPTIONAL,
            name: "ordinary",
            number: 4,
            type: Type.MESSAGE,
            typeName: ".httpwkt.Ordinary",
          },
        ],
        name: "Envelope",
        nestedType: [
          {
            field: [
              {
                jsonName: "key",
                label: Label.OPTIONAL,
                name: "key",
                number: 1,
                type: Type.STRING,
              },
              {
                jsonName: "value",
                label: Label.OPTIONAL,
                name: "value",
                number: 2,
                type: Type.MESSAGE,
                typeName: ".google.protobuf.Timestamp",
              },
            ],
            name: "DeadlinesEntry",
            options: { mapEntry: true },
          },
        ],
      },
      {
        field: [
          {
            jsonName: "fireTimeout",
            label: Label.OPTIONAL,
            name: "fire_timeout",
            number: 1,
            type: Type.MESSAGE,
            typeName: ".google.protobuf.Duration",
          },
        ],
        name: "Child",
      },
      {
        field: [
          {
            jsonName: "seconds",
            label: Label.OPTIONAL,
            name: "seconds",
            number: 1,
            type: Type.STRING,
          },
          {
            jsonName: "nanos",
            label: Label.OPTIONAL,
            name: "nanos",
            number: 2,
            type: Type.INT32,
          },
        ],
        name: "Ordinary",
      },
    ],
    name: "http_wkt_test.proto",
    package: "httpwkt",
    syntax: "proto3",
  });
  const registry = createFileRegistry(descriptor, (name) => {
    if (name === "google/protobuf/timestamp.proto") return TimestampSchema.file;
    if (name === "google/protobuf/duration.proto") return DurationSchema.file;
    return undefined;
  });
  const message = registry.getMessage("httpwkt.Envelope");
  if (message === undefined) throw new Error("missing test descriptor");
  return message;
}

describe("HTTP well-known-type JSON compatibility", () => {
  it("unary HTTP responses decode daemon timestamp objects", async () => {
    const message = await unarySchedules({
      schedules: [
        { spec: { trigger: { one_shot: {} } } },
        { spec: { trigger: { one_shot: { seconds: 0, nanos: 120_000_000 } } } },
        { spec: { trigger: { one_shot: { seconds: "0", nanos: 123_456_000 } } } },
        { spec: { trigger: { one_shot: { seconds: "0", nanos: 123_456_789 } } } },
      ],
    });

    expect(
      message.schedules.map((schedule) => ({
        nanos: schedule.spec?.trigger?.oneShot?.nanos,
        seconds: schedule.spec?.trigger?.oneShot?.seconds,
      })),
    ).toEqual([
      { nanos: 0, seconds: 0n },
      { nanos: 120_000_000, seconds: 0n },
      { nanos: 123_456_000, seconds: 0n },
      { nanos: 123_456_789, seconds: 0n },
    ]);
  });

  it("unary HTTP responses decode daemon duration objects", async () => {
    const message = await unarySchedules({
      schedules: [
        { spec: { fire_timeout: { seconds: 7 } } },
        { spec: { fire_timeout: {} } },
        { spec: { fire_timeout: { nanos: -500_000_000 } } },
        { spec: { fire_timeout: { seconds: "-2", nanos: -250_000_000 } } },
      ],
    });

    expect(
      message.schedules.map((schedule) => ({
        nanos: schedule.spec?.fireTimeout?.nanos,
        seconds: schedule.spec?.fireTimeout?.seconds,
      })),
    ).toEqual([
      { nanos: 0, seconds: 7n },
      { nanos: 0, seconds: 0n },
      { nanos: -500_000_000, seconds: 0n },
      { nanos: -250_000_000, seconds: -2n },
    ]);
  });

  it("normalization follows descriptors through nested list and map values", () => {
    const raw = {
      children: [
        { fire_timeout: { seconds: "1", nanos: 250_000_000 } },
        { fireTimeout: { seconds: -1, nanos: -1 } },
      ],
      createdAt: { seconds: 0, nanos: 123_000_000 },
      deadlines: {
        seconds: { seconds: "2" },
        tomorrow: { seconds: 86_400 },
      },
      ordinary: { nanos: 9, seconds: "kept" },
      unknown: { nanos: 4, seconds: 3 },
    } satisfies JsonValue;

    expect(normalizeHttpWktJson(descriptorFixture(), raw)).toEqual({
      children: [{ fire_timeout: "1.250s" }, { fireTimeout: "-1.000000001s" }],
      createdAt: "1970-01-01T00:00:00.123Z",
      deadlines: {
        seconds: "1970-01-01T00:00:02Z",
        tomorrow: "1970-01-02T00:00:00Z",
      },
      ordinary: { nanos: 9, seconds: "kept" },
      unknown: { nanos: 4, seconds: 3 },
    });
    expect(raw.createdAt).toEqual({ seconds: 0, nanos: 123_000_000 });
    expect(raw.deadlines.seconds).toEqual({ seconds: "2" });
  });

  it("SSE frames decode descriptor-declared well-known types", async () => {
    const rawEvent = {
      authorization: {
        authorization_id: "auth-1",
        expires_at: { seconds: 1, nanos: 123_456_789 },
        status: "pending",
      },
      run_id: "run-1",
      type: "authorization.required",
    } satisfies JsonValue;
    const converse = await sseTransport([rawEvent]).stream(
      HarnessService.method.converse,
      undefined,
      undefined,
      undefined,
      one<MessageInitShape<typeof HarnessService.method.converse.input>>({
        kind: { case: "prompt", value: { sessionId: "session-1", text: "start" } },
      }),
    );
    const converseFrame = await converse.message[Symbol.asyncIterator]().next();
    expect(converseFrame.value?.event?.authorization?.expiresAt).toMatchObject({
      nanos: 123_456_789,
      seconds: 1n,
    });

    const watchRaw = { cursor: "next", event: rawEvent, phase: "replay" } satisfies JsonValue;
    const watch = await sseTransport([watchRaw]).stream(
      HarnessService.method.watchSessionEvents,
      undefined,
      undefined,
      undefined,
      one({ sessionId: "session-1" }),
    );
    const watchFrame = await watch.message[Symbol.asyncIterator]().next();
    expect(watchFrame.value?.event?.authorization?.expiresAt).toMatchObject({
      nanos: 123_456_789,
      seconds: 1n,
    });
  });

  it("ProtoJSON strings and null pass through unchanged", async () => {
    const raw = {
      schedule: {
        spec: {
          created_at: "2026-09-17T12:34:56.123456789Z",
          fire_timeout: "-0.000000001s",
          trigger: { one_shot: null },
        },
      },
    } satisfies JsonValue;
    const message = await unarySchedule(raw);

    expect(message.schedule?.spec?.createdAt).toMatchObject({
      nanos: 123_456_789,
      seconds: 1_789_648_496n,
    });
    expect(message.schedule?.spec?.fireTimeout).toMatchObject({ nanos: -1, seconds: 0n });
    expect(message.schedule?.spec?.trigger?.oneShot).toBeUndefined();
    expect(getRawJson(message)).toEqual(raw);

    const stream = await sseTransport([
      {
        cursor: "one",
        event: {
          authorization: { expires_at: "2026-09-17T12:34:56.123456789Z" },
          type: "authorization.required",
        },
        phase: "replay",
      },
      {
        cursor: "two",
        event: {
          authorization: { expires_at: null },
          type: "authorization.resolved",
        },
        phase: "replay",
      },
    ]).stream(
      HarnessService.method.watchSessionEvents,
      undefined,
      undefined,
      undefined,
      one({ sessionId: "session-1" }),
    );
    const iterator = stream.message[Symbol.asyncIterator]();
    expect((await iterator.next()).value?.event?.authorization?.expiresAt).toMatchObject({
      nanos: 123_456_789,
      seconds: 1_789_648_496n,
    });
    expect((await iterator.next()).value?.event?.authorization?.expiresAt).toBeUndefined();
  });

  it("raw JSON preserves the original daemon wire value", async () => {
    const unaryRaw = {
      future: { nanos: 4, seconds: 3 },
      schedule: {
        spec: {
          fire_timeout: { nanos: 500_000_000, seconds: "4" },
          trigger: { one_shot: { nanos: 123_000_000, seconds: 0 } },
        },
      },
    } satisfies JsonValue;
    const unary = await unarySchedule(unaryRaw);
    expect(getRawJson(unary)).toEqual(unaryRaw);

    const rawEvent = {
      authorization: {
        expires_at: { nanos: 7, seconds: "8" },
        future: "retained",
      },
      type: "authorization.required",
    } satisfies JsonValue;
    const converse = await sseTransport([rawEvent]).stream(
      HarnessService.method.converse,
      undefined,
      undefined,
      undefined,
      one({ kind: { case: "prompt", value: { sessionId: "session-1" } } }),
    );
    const converseMessage = (await converse.message[Symbol.asyncIterator]().next()).value;
    if (converseMessage?.event === undefined) throw new Error("missing Converse event");
    expect(getRawJson(converseMessage)).toEqual(rawEvent);
    expect(getRawJson(converseMessage.event)).toEqual(rawEvent);

    const watchRaw = {
      cursor: "next",
      event: rawEvent,
      future_envelope: true,
      phase: "replay",
    } satisfies JsonValue;
    const watch = await sseTransport([watchRaw]).stream(
      HarnessService.method.watchSessionEvents,
      undefined,
      undefined,
      undefined,
      one({ sessionId: "session-1" }),
    );
    const watchMessage = (await watch.message[Symbol.asyncIterator]().next()).value;
    if (watchMessage?.event === undefined) throw new Error("missing watch event");
    expect(getRawJson(watchMessage.event)).toEqual(rawEvent);
    expect(getRawJson(watchMessage)).toEqual(watchRaw);
  });

  it("invalid well-known type objects fail with typed protocol errors", async () => {
    const invalidValues: JsonValue[] = [
      true,
      1,
      [],
      { extra: 1 },
      { seconds: 1.5 },
      { seconds: Number.MAX_SAFE_INTEGER + 1 },
      { seconds: "+1" },
      { seconds: "01" },
      { seconds: "-0" },
      { seconds: " 1" },
      { nanos: 0.5 },
      { nanos: "1" },
      { nanos: -1 },
      { nanos: 1_000_000_000 },
      { seconds: "-62135596801" },
      { seconds: "253402300800" },
    ];
    for (const value of invalidValues) {
      const operation = unarySchedule({ schedule: { spec: { created_at: value } } });
      await expect(operation, JSON.stringify(value)).rejects.toMatchObject({
        code: "protocol",
        transport: "http",
      });
      await expect(operation).rejects.toBeInstanceOf(ProtocolError);
    }

    const invalidDurations: JsonValue[] = [
      { seconds: "-315576000001" },
      { seconds: "315576000001" },
      { seconds: 1, nanos: -1 },
      { seconds: -1, nanos: 1 },
      { nanos: -1_000_000_000 },
    ];
    for (const value of invalidDurations) {
      await expect(
        unarySchedule({ schedule: { spec: { fire_timeout: value } } }),
        JSON.stringify(value),
      ).rejects.toMatchObject({ code: "protocol", transport: "http" });
    }
  });
});
