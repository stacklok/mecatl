import type { JsonValue } from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";

import { ProtocolError, type TransportKind } from "./errors.js";
import { ContentSchema } from "./gen/mecatl/v1/harness_pb.js";
import { encodePrompt, type PromptCapabilities, type PromptInput } from "./media.js";
import type { RequestOptions } from "./namespaces-core.js";
import { approvalVerdict, type ConverseFrame, type PermissionVerdict } from "./run.js";

/** The server's answer to a steer control. @public */
export type SteerOutcome = "accepted" | "appended" | "too_late";

/** The server's answer to a cancel-steer control. @public */
export type SteerCancelOutcome = "retracted" | "none_pending";

/** Options accepted by the steer and cancel-steer run controls. @public */
export interface SteerControlOptions extends RequestOptions {
  /**
   * Client-minted correlation id. The acknowledgement echoes it, and the
   * run's later `steer` drain echo names the id of the LAST message merged
   * into the drained bundle (the watermark a client splits its queue on).
   */
  messageId?: string;
}

/** The acknowledgement of a steer control. @public */
export interface SteerAck {
  /** The request's own message id, echoed by the server ("" when none). */
  readonly messageId: string;
  readonly outcome: SteerOutcome;
  /** True when a too-late unqualified steer was promoted into a follow-up run. */
  readonly promoted: boolean;
  /** The promoted follow-up run's id, "" when nothing was promoted. */
  readonly runId: string;
}

/** The acknowledgement of a cancel-steer control. @public */
export interface SteerCancelAck {
  readonly messageId: string;
  readonly outcome: SteerCancelOutcome;
}

/**
 * Prompt-free controls for one run, addressed by run id.
 *
 * Every control is STRICT: it names the run through `expected_run_id`, so a
 * control that outlives its run is refused by the server as
 * `stale_run_control` rather than acting on the session's next run. Unlike the
 * controls on a `Run`, these do not require the run's own event stream, so
 * they also serve a run this client re-attached to or observes through a
 * durable watch. HTTP transport only.
 * @public
 */
export interface RunControls {
  readonly runId: string;
  readonly sessionId: string;
  /**
   * Resolves one pending permission ask on this run.
   *
   * @param askId - ID carried by the permission ask.
   * @param verdict - Decision to apply to the pending ask.
   * @param options - Request headers, cancellation signal, and deadline.
   * @throws `ServerError` with code `stale_run_control` when the run already ended.
   */
  resolveAsk(askId: string, verdict: PermissionVerdict, options?: RequestOptions): Promise<void>;
  /**
   * Requests cancellation of this run.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @throws `ServerError` with code `stale_run_control` when the run already ended.
   */
  cancel(options?: RequestOptions): Promise<void>;
  /**
   * Strictly steers this run with text or ordered text and media parts.
   *
   * @param prompt - Instruction to inject at the run's next turn boundary.
   * @param options - Correlation id plus request headers, signal, and deadline.
   * @returns The server's outcome; the caller keeps the text on `too_late`.
   * @throws `UnsupportedFeatureError` when the server lacks `http_steer`.
   * @throws `ServerError` with code `stale_run_control` when the run already ended.
   */
  steer(prompt: PromptInput, options?: SteerControlOptions): Promise<SteerAck>;
  /**
   * Retracts this run's pending, un-drained steer bundle.
   *
   * @param options - Correlation id plus request headers, signal, and deadline.
   * @returns `retracted`, or `none_pending` when nothing was waiting.
   * @throws `UnsupportedFeatureError` when the server lacks `http_steer`.
   */
  cancelSteer(options?: SteerControlOptions): Promise<SteerCancelAck>;
}

/** Internal seam the client hands the controls: one control send plus prompt capabilities. */
export interface RunControlOperations {
  assertOpen(): void;
  control(
    sessionId: string,
    frame: ConverseFrame,
    options?: RequestOptions,
  ): Promise<JsonValue | undefined>;
  promptCapabilities(): PromptCapabilities | undefined;
  readonly transportKind: TransportKind;
}

function ackRecord(value: JsonValue | undefined): Record<string, JsonValue> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  return value as Record<string, JsonValue>;
}

function ackString(record: Record<string, JsonValue>, key: string): string {
  const value = record[key];
  return typeof value === "string" ? value : "";
}

function steerOutcome(value: string, transport: TransportKind): SteerOutcome {
  switch (value) {
    case "accepted":
    case "appended":
    case "too_late":
      return value;
    default:
      throw new ProtocolError(`The steer acknowledgement carried an unknown outcome ${value}`, {
        transport,
      });
  }
}

function steerCancelOutcome(value: string, transport: TransportKind): SteerCancelOutcome {
  switch (value) {
    case "retracted":
    case "none_pending":
      return value;
    default:
      throw new ProtocolError(
        `The cancel-steer acknowledgement carried an unknown outcome ${value}`,
        { transport },
      );
  }
}

class RunControlsImpl implements RunControls {
  readonly runId: string;
  readonly sessionId: string;
  readonly #operations: RunControlOperations;

  constructor(sessionId: string, runId: string, operations: RunControlOperations) {
    this.runId = runId;
    this.sessionId = sessionId;
    this.#operations = operations;
  }

  async resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    options?: RequestOptions,
  ): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.control(
      this.sessionId,
      {
        kind: {
          case: "resumeApproval",
          value: {
            allow: verdict !== "deny",
            askId,
            expectedRunId: this.runId,
            verdict: approvalVerdict(verdict, this.#operations.transportKind),
          },
        },
      },
      options,
    );
  }

  async cancel(options?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.control(
      this.sessionId,
      { kind: { case: "cancel", value: { expectedRunId: this.runId } } },
      options,
    );
  }

  async steer(prompt: PromptInput, options: SteerControlOptions = {}): Promise<SteerAck> {
    this.#operations.assertOpen();
    const { messageId = "", ...requestOptions } = options;
    const encoded = encodePrompt(prompt, this.#operations.promptCapabilities());
    const ack = ackRecord(
      await this.#operations.control(
        this.sessionId,
        {
          kind: {
            case: "steer",
            value: {
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
              text: encoded.text,
            },
          },
        },
        requestOptions,
      ),
    );
    return {
      messageId: ackString(ack, "message_id") || messageId,
      outcome: steerOutcome(ackString(ack, "outcome"), this.#operations.transportKind),
      promoted: ack.promoted === true,
      runId: ackString(ack, "run_id"),
    };
  }

  async cancelSteer(options: SteerControlOptions = {}): Promise<SteerCancelAck> {
    this.#operations.assertOpen();
    const { messageId = "", ...requestOptions } = options;
    const ack = ackRecord(
      await this.#operations.control(
        this.sessionId,
        {
          kind: {
            case: "steerCancel",
            value: { expectedRunId: this.runId, messageId },
          },
        },
        requestOptions,
      ),
    );
    return {
      messageId: ackString(ack, "message_id") || messageId,
      outcome: steerCancelOutcome(ackString(ack, "outcome"), this.#operations.transportKind),
    };
  }
}

/** Creates the run-id-addressed control handle for one session run. */
export function createRunControls(
  sessionId: string,
  runId: string,
  operations: RunControlOperations,
): RunControls {
  return new RunControlsImpl(sessionId, runId, operations);
}
