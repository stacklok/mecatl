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

/**
 * Optional application correlation for a strict steer or retraction request.
 *
 * The server accepts at most 64 Unicode code points and echoes the supplied ID
 * in the operation's acknowledgement. Omission sends an empty correlation ID.
 *
 * @public
 */
export interface RunSteerOptions {
  /** Client-authored correlation ID echoed by the server. */
  messageId?: string;
}

/**
 * The authoritative acknowledgement for a strict steer request.
 *
 * `accepted` means the steer created a pending bundle. `appended` means the
 * steer was merged into the bundle that was already pending. The run and
 * message IDs echo the addressed run and the request correlation.
 *
 * @public
 */
export interface RunSteerAcknowledgement {
  /** Whether the steer created or joined the pending bundle. */
  readonly outcome: "accepted" | "appended";
  /** Exact run ID addressed by the request. */
  readonly runId: string;
  /** Request correlation ID, or an empty string when none was supplied. */
  readonly messageId: string;
}

/**
 * The authoritative acknowledgement for strict steer retraction.
 *
 * `retracted` means the pending bundle was removed. `none_pending` means the
 * exact live run had no pending bundle at the transition point. The run and
 * message IDs echo the addressed run and the request correlation.
 *
 * @public
 */
export interface RunSteerCancellationAcknowledgement {
  /** Whether a pending steer bundle was removed. */
  readonly outcome: "retracted" | "none_pending";
  /** Exact run ID addressed by the request. */
  readonly runId: string;
  /** Request correlation ID, or an empty string when none was supplied. */
  readonly messageId: string;
}

/**
 * Prompt-free controls bound to one exact session run.
 *
 * Construct this resource with {@link Session.controls}. It does not attach,
 * subscribe, or keep a run alive. Every method requires the server's
 * `prompt_free_controls` feature, addresses `runId` exactly, performs one unary
 * request without automatic retry, and accepts ordinary {@link RequestOptions}.
 * A server that lacks the feature raises {@link UnsupportedFeatureError} before
 * a control RPC is sent. Ended, cancelling, replaced, or otherwise stale runs
 * fail with the server's typed `stale_run_control` error.
 *
 * A transport failure, caller cancellation, or deadline after dispatch can
 * reject the promise after the server accepted the operation. Reconcile that
 * ambiguous case from the authoritative session or activity state before
 * deciding whether to retry.
 *
 * @public
 */
export interface RunControls {
  /** Session that owns the addressed run. */
  readonly sessionId: string;
  /** Exact durable run addressed by every operation. */
  readonly runId: string;
  /**
   * Resolves one ordinary permission ask on this exact run.
   *
   * Root and surfaced-child permission asks are supported, including an
   * ordinary ask restored from a persisted awaiting run. Plan-originated asks
   * require `Session.resolvePlan()` and fail with `plan_resolution_required`.
   * Unknown or already resolved asks fail with `ask_not_pending`. Unlike the
   * stream-local `Run.resolveAsk()` send-only operation, this control returns
   * only after the server acknowledges acceptance.
   *
   * @param askId - Exact permission ask ID.
   * @param verdict - Ordinary permission verdict to apply.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   */
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  /**
   * Requests cancellation of this exact live run.
   *
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   */
  cancel(requestOptions?: RequestOptions): Promise<void>;
  /**
   * Injects text or ordered media into this exact live run.
   *
   * Structured prompt text fragments are joined with a newline, and media
   * parts retain their order relative to other media. An empty prompt is
   * rejected locally. A late steer fails as stale and never creates a successor
   * run.
   *
   * @param prompt - Text, image, audio, or a structured prompt to inject.
   * @param options - Optional message correlation.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns The server's accepted-or-appended acknowledgement.
   */
  steer(
    prompt: PromptInput,
    options?: RunSteerOptions,
    requestOptions?: RequestOptions,
  ): Promise<RunSteerAcknowledgement>;
  /**
   * Retracts this exact live run's pending steer bundle.
   *
   * @param options - Optional message correlation for this retraction request.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns Whether the server retracted a bundle or found none pending.
   */
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
