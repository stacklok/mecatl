import { Ajv2020, type ValidateFunction } from "ajv/dist/2020.js";

import type { Client, SessionMcpServer } from "./client.js";
import { InvalidStateError, MecatlError, UnsupportedFeatureError } from "./errors.js";

/** The JSON values accepted by callback tool schemas and handlers. @public */
export type ToolJsonValue =
  | boolean
  | number
  | string
  | null
  | readonly ToolJsonValue[]
  | { readonly [key: string]: ToolJsonValue };

/** A plain JSON Schema 2020-12 value; no schema-builder library is required. @public */
export type ToolSchema = boolean | Readonly<Record<string, ToolJsonValue>>;

/** One JSON-serializable MCP content block returned by a callback tool. @public */
export type CallToolContent = Readonly<Record<string, ToolJsonValue>> & {
  readonly type: string;
};

/** An explicit MCP callback-tool result, including intentional error results. @public */
export interface CallToolResult {
  readonly content: readonly CallToolContent[];
  readonly isError?: boolean;
  readonly structuredContent?: ToolJsonValue;
}

/** Context supplied to one callback tool invocation. @public */
export interface ToolHandlerContext {
  /** Aborted when the host cancels this invocation or shuts down. */
  readonly signal: AbortSignal;
}

/** A locally registered callback tool implementation. @public */
export type ToolHandler = (
  arguments_: Readonly<Record<string, ToolJsonValue>>,
  context: ToolHandlerContext,
) => unknown | Promise<unknown>;

/** Registration options for one callback tool. @public */
export interface ToolOptions {
  /**
   * Tightens the client-wide handler concurrency cap for this tool.
   * Values above the client cap never raise it.
   */
  concurrency?: number;
  /**
   * Unverified caller assertion that the callback has no side effects.
   *
   * The SDK carries this as MCP's `readOnlyHint`; mecatl trusts that hint when
   * placing calls in its concurrent read batch. A mis-annotated callback may
   * therefore run concurrently, and MCP names are not in plan mode's fixed
   * mutation-deny set.
   */
  readOnly?: boolean;
}

/** The immutable public description returned for a registered callback tool. @public */
export interface ToolDefinition {
  /** Per-tool handler concurrency cap, when one was requested. */
  readonly concurrency: number | undefined;
  /** Model-facing name after applying the client-wide MCP server namespace. */
  readonly modelName: string;
  /** Name advertised by the local MCP server. */
  readonly name: string;
  /** The unverified read-only assertion carried as MCP `readOnlyHint`. */
  readonly readOnly: boolean;
  /** The JSON Schema 2020-12 value advertised for tool arguments. */
  readonly schema: ToolSchema;
}

/** A Node/Bun Client with the local callback-tool registration surface. @public */
export interface NodeClient extends Client {
  /** Registers one callback tool in the client-wide immutable tool set. */
  tool(
    name: string,
    schema: ToolSchema,
    handler: ToolHandler,
    options?: ToolOptions,
  ): ToolDefinition;
}

/** Stable authoring-error reasons carried by ToolRegistrationError. @public */
export type ToolRegistrationReason =
  | "duplicate_name"
  | "invalid_options"
  | "invalid_schema"
  | "invalid_server_name"
  | "invalid_tool_name";

/** A callback tool could not be added to the client registry. @public */
export class ToolRegistrationError extends MecatlError {
  /** Stable reason distinguishing the rejected registration input. */
  readonly reason: ToolRegistrationReason;

  constructor(reason: ToolRegistrationReason, message: string, cause?: unknown) {
    super(message, {
      ...(cause === undefined ? {} : { cause }),
      code: "tool_registration",
      transport: "local",
    });
    this.reason = reason;
  }
}

export const DEFAULT_TOOL_SERVER_NAME = "sdk";

// BEGIN MECATL_CLIENT_SERVER_NAME_RULES
export const CLIENT_SERVER_NAME_PATTERN = /^[A-Za-z0-9._-]+$/;
export const MAX_CLIENT_SERVERS = 8;
export const MAX_CLIENT_SERVER_NAME_LEN = 64;
// END MECATL_CLIENT_SERVER_NAME_RULES

export const SDK_MCP_SERVER_SPEC_FIELDS = [
  // BEGIN MECATL_MCP_SERVER_SPEC_FIELDS
  "name",
  "url",
  "type",
  "command",
  "headers",
  // END MECATL_MCP_SERVER_SPEC_FIELDS
] as const satisfies readonly (keyof SessionMcpServer)[];

interface RegisteredTool {
  readonly definition: ToolDefinition;
  readonly handler: ToolHandler;
  readonly validate: ValidateFunction;
}

export interface AdvertisedTool {
  readonly annotations: { readonly readOnlyHint: boolean };
  readonly inputSchema: ToolSchema;
  readonly name: string;
}

export interface ValidationErrorResult extends CallToolResult {
  readonly content: readonly [{ readonly text: string; readonly type: "text" }];
  readonly isError: true;
}

export interface ToolHostBinding {
  abort(reason: unknown): void;
  mcpServer(): SessionMcpServer;
  start(): Promise<void> | void;
  stop(): Promise<void>;
}

export interface ToolSessionLease {
  finish(created: boolean): void;
}

function validateServerName(name: string): void {
  if (
    name === "" ||
    name.length > MAX_CLIENT_SERVER_NAME_LEN ||
    name.includes("__") ||
    !CLIENT_SERVER_NAME_PATTERN.test(name)
  ) {
    throw new ToolRegistrationError(
      "invalid_server_name",
      `Tool server name ${JSON.stringify(name)} must be non-empty, at most ${MAX_CLIENT_SERVER_NAME_LEN} characters, use only [A-Za-z0-9._-], and not contain "__"`,
    );
  }
}

function validateToolName(name: string): void {
  if (name === "" || name.includes("__")) {
    throw new ToolRegistrationError(
      "invalid_tool_name",
      `Tool name ${JSON.stringify(name)} must be non-empty and not contain "__"`,
    );
  }
}

function validateConcurrency(value: number | undefined): void {
  if (value !== undefined && (!Number.isSafeInteger(value) || value <= 0)) {
    throw new ToolRegistrationError(
      "invalid_options",
      "Tool concurrency must be a positive safe integer",
    );
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isJsonValue(value: unknown, ancestors = new Set<object>()): value is ToolJsonValue {
  if (value === null || typeof value === "string" || typeof value === "boolean") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (typeof value !== "object") return false;
  if (ancestors.has(value)) return false;
  ancestors.add(value);
  const valid = Array.isArray(value)
    ? value.every((item) => isJsonValue(item, ancestors))
    : Object.getPrototypeOf(value) === Object.prototype &&
      Object.values(value).every((item) => isJsonValue(item, ancestors));
  ancestors.delete(value);
  return valid;
}

function copyJson<T extends ToolJsonValue>(value: T): T {
  if (Array.isArray(value)) {
    return Object.freeze(value.map((item) => copyJson(item))) as unknown as T;
  }
  if (typeof value !== "object" || value === null) return value;
  const copy = Object.create(null) as Record<string, ToolJsonValue>;
  for (const [key, item] of Object.entries(value)) {
    Object.defineProperty(copy, key, {
      configurable: false,
      enumerable: true,
      value: copyJson(item),
      writable: false,
    });
  }
  return Object.freeze(copy) as T;
}

function schemaCopy(schema: ToolSchema): ToolSchema {
  if (!isJsonValue(schema) || (typeof schema === "object" && !isRecord(schema))) {
    throw new ToolRegistrationError(
      "invalid_schema",
      "Tool schema must be a plain JSON Schema 2020-12 object or boolean",
    );
  }
  return copyJson(schema);
}

function validationMessage(name: string, validate: ValidateFunction): string {
  const details = (validate.errors ?? [])
    .map((error) => `${error.instancePath || "/"} ${error.message ?? "is invalid"}`)
    .join("; ");
  return `Arguments for tool ${JSON.stringify(name)} do not match its schema${details === "" ? "" : `: ${details}`}`;
}

export class ToolRegistry {
  readonly serverName: string;
  readonly #binding: ToolHostBinding;
  readonly #tools = new Map<string, RegisteredTool>();
  #creates = 0;
  #sessionCreated = false;

  constructor(serverName: string, binding: ToolHostBinding) {
    validateServerName(serverName);
    this.serverName = serverName;
    this.#binding = binding;
  }

  abort(reason: unknown): void {
    this.#binding.abort(reason);
  }

  advertisedTools(): readonly AdvertisedTool[] {
    return [...this.#tools.values()].map(({ definition }) => ({
      annotations: { readOnlyHint: definition.readOnly },
      inputSchema: definition.schema,
      name: definition.name,
    }));
  }

  beginSessionCreate(): ToolSessionLease {
    this.#creates += 1;
    let finished = false;
    return {
      finish: (created) => {
        if (finished) return;
        finished = true;
        this.#creates -= 1;
        this.#sessionCreated ||= created;
      },
    };
  }

  hasTools(): boolean {
    return this.#tools.size > 0;
  }

  concurrencyFor(name: string): number | undefined {
    return this.#tools.get(name)?.definition.concurrency;
  }

  async invoke(
    name: string,
    arguments_: unknown,
    signal: AbortSignal = new AbortController().signal,
  ): Promise<unknown | ValidationErrorResult> {
    const registered = this.#tools.get(name);
    if (registered === undefined) {
      return {
        content: [{ text: `Unknown callback tool ${JSON.stringify(name)}`, type: "text" }],
        isError: true,
      };
    }
    if (!registered.validate(arguments_)) {
      return {
        content: [
          {
            text: validationMessage(name, registered.validate),
            type: "text",
          },
        ],
        isError: true,
      };
    }
    if (!isRecord(arguments_) || !isJsonValue(arguments_)) {
      return {
        content: [
          {
            text: `Arguments for tool ${JSON.stringify(name)} must be a JSON object`,
            type: "text",
          },
        ],
        isError: true,
      };
    }
    return registered.handler(copyJson(arguments_), { signal });
  }

  mcpServer(): SessionMcpServer {
    return { ...this.#binding.mcpServer(), name: this.serverName };
  }

  register(
    name: string,
    schema: ToolSchema,
    handler: ToolHandler,
    options: ToolOptions = {},
  ): ToolDefinition {
    if (this.#sessionCreated || this.#creates > 0) {
      throw new InvalidStateError(
        "The callback tool set is immutable once session creation begins",
        {
          transport: "local",
        },
      );
    }
    validateToolName(name);
    validateConcurrency(options.concurrency);
    if (this.#tools.has(name)) {
      throw new ToolRegistrationError(
        "duplicate_name",
        `Callback tool ${JSON.stringify(name)} is already registered`,
      );
    }

    const copiedSchema = schemaCopy(schema);
    let validate: ValidateFunction;
    try {
      const ajv = new Ajv2020({
        allErrors: true,
        coerceTypes: false,
        removeAdditional: false,
        strict: false,
        useDefaults: false,
        validateSchema: true,
      });
      validate = ajv.compile(copiedSchema);
    } catch (cause) {
      throw new ToolRegistrationError(
        "invalid_schema",
        `Callback tool ${JSON.stringify(name)} has an invalid JSON Schema 2020-12 schema`,
        cause,
      );
    }

    const definition: ToolDefinition = Object.freeze({
      concurrency: options.concurrency,
      modelName: `mcp__${this.serverName}__${name}`,
      name,
      readOnly: options.readOnly === true,
      schema: copiedSchema,
    });
    this.#tools.set(name, { definition, handler, validate });
    return definition;
  }

  start(): Promise<void> | void {
    return this.#binding.start();
  }

  stop(): Promise<void> {
    return this.#binding.stop();
  }
}

export function withToolRegistration(
  client: Client,
  registry?: ToolRegistry,
  missingFeature = "local_callback_tools",
): NodeClient {
  Object.defineProperty(client, "tool", {
    configurable: false,
    enumerable: true,
    value: (name: string, schema: ToolSchema, handler: ToolHandler, options?: ToolOptions) => {
      if (registry === undefined) {
        throw new UnsupportedFeatureError(missingFeature, { transport: "local" });
      }
      return registry.register(name, schema, handler, options);
    },
    writable: false,
  });
  return client as NodeClient;
}
