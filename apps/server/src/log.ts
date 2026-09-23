// SPDX-License-Identifier: Apache-2.0

import type { LogLevel } from "./config.js";

export type LogFields = Readonly<Record<string, boolean | number | string | null | undefined>>;

export interface LogRecord {
  readonly audit?: true;
  readonly event: string;
  readonly fields: LogFields;
  readonly level: LogLevel;
  readonly time: string;
}

export interface Logger {
  /** Structured security-audit record; always emitted regardless of level. */
  audit(event: string, fields?: LogFields): void;
  debug(event: string, fields?: LogFields): void;
  error(event: string, fields?: LogFields): void;
  info(event: string, fields?: LogFields): void;
  warn(event: string, fields?: LogFields): void;
}

const levelRank: Record<LogLevel, number> = { debug: 0, error: 3, info: 1, warn: 2 };

/**
 * JSON-lines logger on stderr. Audit records carry `audit: true` and are never
 * filtered by level; callers must never place token material in fields.
 */
export function createLogger(
  level: LogLevel = "info",
  sink: (record: LogRecord) => void = writeStderr,
  now: () => Date = () => new Date(),
): Logger {
  const emit = (recordLevel: LogLevel, event: string, fields: LogFields, audit?: true) => {
    if (audit === undefined && levelRank[recordLevel] < levelRank[level]) return;
    sink({
      ...(audit === undefined ? {} : { audit }),
      event,
      fields: compact(fields),
      level: recordLevel,
      time: now().toISOString(),
    });
  };
  return {
    audit: (event, fields = {}) => emit("info", event, fields, true),
    debug: (event, fields = {}) => emit("debug", event, fields),
    error: (event, fields = {}) => emit("error", event, fields),
    info: (event, fields = {}) => emit("info", event, fields),
    warn: (event, fields = {}) => emit("warn", event, fields),
  };
}

export const silentLogger: Logger = createLogger("error", () => undefined);

function compact(fields: LogFields): LogFields {
  return Object.fromEntries(Object.entries(fields).filter(([, value]) => value !== undefined));
}

function writeStderr(record: LogRecord): void {
  process.stderr.write(`${JSON.stringify(record)}\n`);
}
