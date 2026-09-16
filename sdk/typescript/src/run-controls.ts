import type {
  DescMessage,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";

import {
  InvalidStateError,
  PromptValidationError,
  ProtocolError,
  type TransportKind,
  UnsupportedFeatureError,
} from "./errors.js";
import {
  ApprovalVerdict,
  type CancelRunSteerResponse,
  ContentSchema,
  HarnessService,
  type ResolveRunAskResponse,
  SteerOutcome,
  type SteerRunResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import { encodePrompt, type PromptCapabilities, type PromptInput } from "./media.js";
import type { RequestOptions } from "./namespaces-core.js";
import { getRawJson } from "./raw.js";
import type { PermissionVerdict } from "./run.js";
import { ServerFeature } from "./server.js";

type SteerControlResponse = SteerRunResponse | CancelRunSteerResponse;

/** Optional correlation for one strict steer operation. @public */
export interface RunSteerOptions {
  /** Client-authored message ID echoed by the server. Omission sends an empty correlation. */
  messageId?: string;
}

/** A narrowed acknowledgement for an accepted strict steer. @public */
export interface RunSteerAcknowledgement {
  readonly outcome: "accepted" | "appended";
  readonly runId: string;
  readonly messageId: string;
}

/** A narrowed acknowledgement for strict steer retraction. @public */
export interface RunSteerCancellationAcknowledgement {
  readonly outcome: "retracted" | "none_pending";
  readonly runId: string;
  readonly messageId: string;
}

/** Prompt-free controls bound to one exact session run. @public */
export interface RunControls {
  readonly sessionId: string;
  readonly runId: string;
  /** Resolves one ordinary permission ask on this exact run. */
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  /** Requests cancellation of this exact run. */
  cancel(requestOptions?: RequestOptions): Promise<void>;
  /** Injects text or ordered media into this exact live run. */
  steer(
    prompt: PromptInput,
    options?: RunSteerOptions,
    requestOptions?: RequestOptions,
  ): Promise<RunSteerAcknowledgement>;
  /** Retracts this exact run's pending steer bundle. */
  cancelSteer(
    options?: RunSteerOptions,
    requestOptions?: RequestOptions,
  ): Promise<RunSteerCancellationAcknowledgement>;
}

interface RunControlOperations {
  assertOpen(): void;
  features(options?: RequestOptions): Promise<ReadonlySet<string>>;
  promptCapabilities(): PromptCapabilities | undefined;
  readonly transportKind: TransportKind;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: RequestOptions,
  ): Promise<MessageShape<O>>;
}

function permissionVerdict(verdict: PermissionVerdict, transport: TransportKind): ApprovalVerdict {
  switch (verdict) {
    case "deny":
      return ApprovalVerdict.DENY;
    case "allow_once":
      return ApprovalVerdict.ALLOW_ONCE;
    case "allow_always":
      return ApprovalVerdict.ALLOW_ALWAYS;
    default:
      throw new InvalidStateError(`Unknown permission verdict: ${String(verdict)}`, { transport });
  }
}

function rawRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function hasOwn(record: Record<string, unknown>, name: string): boolean {
  return Object.hasOwn(record, name);
}

function protocol(message: string, transport: TransportKind): never {
  throw new ProtocolError(message, { transport });
}

function validateStringField(
  response: unknown,
  raw: Record<string, unknown> | undefined,
  property: string,
  jsonProperty: string,
  transport: TransportKind,
): string {
  if (transport === "http") {
    if (raw === undefined || !hasOwn(raw, jsonProperty) || typeof raw[jsonProperty] !== "string") {
      protocol(`Run control response is missing string field ${jsonProperty}`, transport);
    }
    return raw[jsonProperty] as string;
  }
  const record = rawRecord(response);
  const value = record?.[property];
  if (typeof value !== "string") {
    protocol(`Run control response is missing string field ${property}`, transport);
  }
  return value;
}

function validateCorrelation(
  response: unknown,
  raw: Record<string, unknown> | undefined,
  expectedRunId: string,
  expectedMessageId: string | undefined,
  transport: TransportKind,
): { readonly messageId?: string; readonly runId: string } {
  const runId = validateStringField(response, raw, "runId", "run_id", transport);
  if (runId !== expectedRunId)
    protocol("Run control response changed the addressed run ID", transport);
  if (expectedMessageId === undefined) return { runId };
  const messageId = validateStringField(response, raw, "messageId", "message_id", transport);
  if (messageId !== expectedMessageId) {
    protocol("Run control response changed the message correlation ID", transport);
  }
  return { messageId, runId };
}

function rawResponse(
  response: object,
  transport: TransportKind,
): Record<string, unknown> | undefined {
  if (transport !== "http") return undefined;
  const raw = rawRecord(getRawJson(response));
  if (raw === undefined)
    protocol("Run control response has no raw HTTP acknowledgement", transport);
  return raw;
}

function validateNoPromotion(
  response: unknown,
  raw: Record<string, unknown> | undefined,
  transport: TransportKind,
): void {
  if (raw !== undefined && hasOwn(raw, "promoted")) {
    protocol("Strict run controls cannot acknowledge promoted steering", transport);
  }
  const record = rawRecord(response);
  if (record !== undefined && hasOwn(record, "promoted")) {
    protocol("Strict run controls cannot acknowledge promoted steering", transport);
  }
}

class RunControlsImpl implements RunControls {
  readonly sessionId: string;
  readonly runId: string;
  readonly #operations: RunControlOperations;

  constructor(sessionId: string, runId: string, operations: RunControlOperations) {
    this.sessionId = sessionId;
    this.runId = runId;
    this.#operations = operations;
  }

  async resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void> {
    this.#operations.assertOpen();
    await this.#requireFeature(requestOptions);
    const response = (await this.#operations.unary(
      HarnessService.method.resolveRunAsk,
      {
        askId,
        expectedRunId: this.runId,
        sessionId: this.sessionId,
        verdict: permissionVerdict(verdict, this.#operations.transportKind),
      },
      requestOptions,
    )) as ResolveRunAskResponse;
    const raw = rawResponse(response, this.#operations.transportKind);
    validateCorrelation(response, raw, this.runId, undefined, this.#operations.transportKind);
    const echoedAskId = validateStringField(
      response,
      raw,
      "askId",
      "ask_id",
      this.#operations.transportKind,
    );
    if (echoedAskId !== askId) {
      protocol(
        "ResolveRunAsk response changed the ask correlation ID",
        this.#operations.transportKind,
      );
    }
  }

  async cancel(requestOptions?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    await this.#requireFeature(requestOptions);
    const response = (await this.#operations.unary(
      HarnessService.method.cancelRun,
      { expectedRunId: this.runId, sessionId: this.sessionId },
      requestOptions,
    )) as object;
    validateCorrelation(
      response,
      rawResponse(response, this.#operations.transportKind),
      this.runId,
      undefined,
      this.#operations.transportKind,
    );
  }

  async steer(
    prompt: PromptInput,
    options: RunSteerOptions = {},
    requestOptions?: RequestOptions,
  ): Promise<RunSteerAcknowledgement> {
    this.#operations.assertOpen();
    const encoded = encodePrompt(prompt, this.#operations.promptCapabilities());
    if (encoded.text.length === 0 && encoded.media.length === 0) {
      throw new PromptValidationError("prompt", "prompt must contain text or media");
    }
    await this.#requireFeature(requestOptions);
    const messageId = options.messageId ?? "";
    const response = (await this.#operations.unary(
      HarnessService.method.steerRun,
      {
        expectedRunId: this.runId,
        messageId,
        parts: encoded.media.map((part) =>
          create(ContentSchema, {
            ...(part.bytes === undefined ? {} : { data: part.bytes }),
            kind: part.kind === "image" ? 1 : 2,
            mimeType: part.mimeType,
            ...(part.url === undefined ? {} : { url: part.url }),
          }),
        ),
        sessionId: this.sessionId,
        text: encoded.text,
      },
      requestOptions,
    )) as SteerRunResponse;
    return this.#steerAcknowledgement(response, messageId);
  }

  async cancelSteer(
    options: RunSteerOptions = {},
    requestOptions?: RequestOptions,
  ): Promise<RunSteerCancellationAcknowledgement> {
    this.#operations.assertOpen();
    await this.#requireFeature(requestOptions);
    const messageId = options.messageId ?? "";
    const response = await this.#operations.unary(
      HarnessService.method.cancelRunSteer,
      { expectedRunId: this.runId, messageId, sessionId: this.sessionId },
      requestOptions,
    );
    const raw = rawResponse(response, this.#operations.transportKind);
    validateNoPromotion(response, raw, this.#operations.transportKind);
    const correlation = validateCorrelation(
      response,
      raw,
      this.runId,
      messageId,
      this.#operations.transportKind,
    );
    const outcome = this.#outcome(response, raw);
    if (outcome !== SteerOutcome.RETRACTED && outcome !== SteerOutcome.NONE_PENDING) {
      protocol(
        "CancelRunSteer returned an outcome for a different operation",
        this.#operations.transportKind,
      );
    }
    return {
      messageId: correlation.messageId ?? "",
      outcome: outcome === SteerOutcome.RETRACTED ? "retracted" : "none_pending",
      runId: correlation.runId,
    };
  }

  async #requireFeature(requestOptions?: RequestOptions): Promise<void> {
    const features = await this.#operations.features(requestOptions);
    if (!features.has(ServerFeature.PromptFreeControls)) {
      throw new UnsupportedFeatureError(ServerFeature.PromptFreeControls, {
        transport: this.#operations.transportKind,
      });
    }
  }

  #outcome(response: SteerControlResponse, raw: Record<string, unknown> | undefined): SteerOutcome {
    if (this.#operations.transportKind === "http") {
      if (raw === undefined || !hasOwn(raw, "outcome") || typeof raw.outcome !== "string") {
        protocol(
          "Run control response is missing string field outcome",
          this.#operations.transportKind,
        );
      }
      switch (raw.outcome) {
        case "accepted":
          return SteerOutcome.ACCEPTED;
        case "appended":
          return SteerOutcome.APPENDED;
        case "retracted":
          return SteerOutcome.RETRACTED;
        case "none_pending":
          return SteerOutcome.NONE_PENDING;
        default:
          protocol(
            "Run control response contains an unknown outcome",
            this.#operations.transportKind,
          );
      }
    }
    if (typeof response.outcome !== "number" || !Number.isInteger(response.outcome)) {
      protocol("Run control response contains a malformed outcome", this.#operations.transportKind);
    }
    return response.outcome;
  }

  #steerAcknowledgement(response: SteerRunResponse, messageId: string): RunSteerAcknowledgement {
    const raw = rawResponse(response, this.#operations.transportKind);
    validateNoPromotion(response, raw, this.#operations.transportKind);
    const correlation = validateCorrelation(
      response,
      raw,
      this.runId,
      messageId,
      this.#operations.transportKind,
    );
    const outcome = this.#outcome(response, raw);
    if (outcome !== SteerOutcome.ACCEPTED && outcome !== SteerOutcome.APPENDED) {
      protocol(
        "SteerRun returned an outcome for a different operation",
        this.#operations.transportKind,
      );
    }
    return {
      messageId: correlation.messageId ?? "",
      outcome: outcome === SteerOutcome.ACCEPTED ? "accepted" : "appended",
      runId: correlation.runId,
    };
  }
}

/** Creates a local run-control binding without probing or attaching. */
export function createRunControls(
  sessionId: string,
  runId: string,
  operations: RunControlOperations,
): RunControls {
  return new RunControlsImpl(sessionId, runId, operations);
}
