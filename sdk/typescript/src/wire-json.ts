import type { DescField, DescMessage, JsonObject, JsonValue } from "@bufbuild/protobuf";

/**
 * The daemon's HTTP surface marshals responses with Go's stdlib encoding/json
 * over the generated proto structs, not protojson. Every scalar and enum still
 * decodes (snake_case names, integer enums, int64 as number), but the two
 * well-known types disagree structurally: a `google.protobuf.Timestamp`
 * arrives as `{"seconds": N, "nanos": M}` and a `google.protobuf.Duration` as
 * `{"seconds": N, "nanos": M}`, whereas protobuf-es `fromJson` accepts only
 * the RFC 3339 string and the `"1.5s"` string respectively.
 *
 * This walker rewrites exactly those two shapes, guided by the response
 * message descriptor, so the transport speaks the daemon's actual encoding —
 * the same discipline as its enum and permission-mode translation. Values that
 * already are protojson strings pass through untouched, and the input is never
 * mutated (the exact received JSON stays registered for `getRawJson`).
 */

const TIMESTAMP = "google.protobuf.Timestamp";
const DURATION = "google.protobuf.Duration";

function isObject(value: JsonValue | undefined): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function structSeconds(value: JsonObject): { nanos: number; seconds: bigint } | undefined {
  for (const key of Object.keys(value)) {
    if (key !== "seconds" && key !== "nanos") return undefined;
  }
  const seconds = value.seconds;
  const nanos = value.nanos;
  let secondsValue: bigint;
  if (seconds === undefined) secondsValue = 0n;
  else if (typeof seconds === "number" && Number.isInteger(seconds)) secondsValue = BigInt(seconds);
  else if (typeof seconds === "string" && /^-?\d+$/.test(seconds)) secondsValue = BigInt(seconds);
  else return undefined;
  let nanosValue: number;
  if (nanos === undefined) nanosValue = 0;
  else if (typeof nanos === "number" && Number.isInteger(nanos)) nanosValue = nanos;
  else if (typeof nanos === "string" && /^-?\d+$/.test(nanos)) nanosValue = Number(nanos);
  else return undefined;
  return { nanos: nanosValue, seconds: secondsValue };
}

function fraction(nanos: number): string {
  if (nanos === 0) return "";
  const digits = String(Math.abs(nanos)).padStart(9, "0");
  const trimmed = digits.endsWith("000000")
    ? digits.slice(0, 3)
    : digits.endsWith("000")
      ? digits.slice(0, 6)
      : digits;
  return `.${trimmed}`;
}

function timestampString(value: JsonObject): JsonValue | undefined {
  const parts = structSeconds(value);
  if (parts === undefined) return undefined;
  const millis = Number(parts.seconds) * 1000;
  if (!Number.isSafeInteger(millis)) return undefined;
  const date = new Date(millis);
  if (Number.isNaN(date.getTime())) return undefined;
  const base = date.toISOString().slice(0, 19);
  return `${base}${fraction(parts.nanos)}Z`;
}

function durationString(value: JsonObject): JsonValue | undefined {
  const parts = structSeconds(value);
  if (parts === undefined) return undefined;
  const negative = parts.seconds < 0n || parts.nanos < 0;
  const seconds = parts.seconds < 0n ? -parts.seconds : parts.seconds;
  return `${negative ? "-" : ""}${seconds.toString()}${fraction(parts.nanos)}s`;
}

function normalizeMessage(desc: DescMessage, value: JsonValue): JsonValue {
  if (!isObject(value)) return value;
  if (desc.typeName === TIMESTAMP) return timestampString(value) ?? value;
  if (desc.typeName === DURATION) return durationString(value) ?? value;
  let copy: JsonObject | undefined;
  for (const field of desc.fields) {
    const key =
      field.name in value ? field.name : field.jsonName in value ? field.jsonName : undefined;
    if (key === undefined) continue;
    const current = value[key];
    if (current === undefined) continue;
    const next = normalizeField(field, current);
    if (next !== current) {
      copy ??= { ...value };
      copy[key] = next;
    }
  }
  return copy ?? value;
}

function normalizeField(field: DescField, value: JsonValue): JsonValue {
  switch (field.fieldKind) {
    case "message":
      return normalizeMessage(field.message, value);
    case "list": {
      if (field.listKind !== "message" || !Array.isArray(value)) return value;
      let changed = false;
      const items = value.map((item) => {
        const next = normalizeMessage(field.message, item);
        if (next !== item) changed = true;
        return next;
      });
      return changed ? items : value;
    }
    case "map": {
      if (field.mapKind !== "message" || !isObject(value)) return value;
      let copy: JsonObject | undefined;
      for (const [key, item] of Object.entries(value)) {
        const next = normalizeMessage(field.message, item);
        if (next !== item) {
          copy ??= { ...value };
          copy[key] = next;
        }
      }
      return copy ?? value;
    }
    default:
      return value;
  }
}

/**
 * Rewrites stdlib-JSON well-known types inside a response into their protojson
 * spellings so `fromJson(desc, …)` accepts them. Returns the input itself when
 * nothing needed rewriting.
 */
export function normalizeWellKnownJson(desc: DescMessage, value: JsonValue): JsonValue {
  return normalizeMessage(desc, value);
}
