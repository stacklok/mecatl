import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";

import { decodeEvent } from "../src/events.js";
import {
  EventSchema,
  GetSessionTranscriptResponseSchema,
  ServerCapabilitiesSchema,
  SessionSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  projectServerCapabilities,
  projectSessionSnapshot,
  projectSessionTranscript,
} from "../src/session-projections.js";

it("projects PDF references and capabilities from events, snapshots, and transcripts", () => {
  const part = {
    artifactId: "pdf-1",
    kind: 3,
    mimeType: "application/pdf",
    name: "report.pdf",
    sha256: "a".repeat(64),
    size: 123n,
  };
  const block = {
    artifactId: "pdf-2",
    kind: 7,
    mimeType: "application/pdf",
    name: "tool.pdf",
    sha256: "b".repeat(64),
    size: 456n,
  };
  const snapshot = projectSessionSnapshot(
    create(SessionSchema, { sessionId: "s", sessionCapabilities: { pdf: true } }),
    "s",
  );
  expect(snapshot?.sessionCapabilities?.pdf).toBe(true);
  expect(
    projectServerCapabilities(create(ServerCapabilitiesSchema, { artifacts: true })).artifacts,
  ).toBe(true);

  const transcript = projectSessionTranscript(
    create(GetSessionTranscriptResponseSchema, {
      sessionId: "s",
      messages: [{ parts: [part], toolResult: { blocks: [block] } }],
    }),
    "s",
  );
  expect(transcript?.messages[0]?.parts[0]).toMatchObject(part);
  expect(transcript?.messages[0]?.toolResult?.blocks[0]).toMatchObject(block);

  const promptEvent = decodeEvent(
    create(EventSchema, { type: "user_prompt", userPrompt: { parts: [part] } }),
    "grpc",
  );
  if (promptEvent.kind !== "user_prompt") throw new Error("wrong prompt event kind");
  expect(promptEvent.payload.parts[0]).toMatchObject(part);
  const resultEvent = decodeEvent(
    create(EventSchema, { type: "tool.result", toolResult: { blocks: [block] } }),
    "grpc",
  );
  if (resultEvent.kind !== "tool.result") throw new Error("wrong result event kind");
  expect(resultEvent.payload.blocks[0]).toMatchObject(block);
});
