import type { DescField, DescMessage, JsonValue } from "@bufbuild/protobuf";
import { create, toJson } from "@bufbuild/protobuf";
import { DurationSchema, TimestampSchema } from "@bufbuild/protobuf/wkt";

type JsonRecord = Record<string, JsonValue>;

const timestampTypeName = "google.protobuf.Timestamp";
const durationTypeName = "google.protobuf.Duration";
const decimalInteger = /^(?:0|-?[1-9][0-9]*)$/;

function isRecord(value: JsonValue): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function cloneJson(value: JsonValue): JsonValue {
  if (Array.isArray(value)) return value.map(cloneJson);
  if (!isRecord(value)) return value;
  return Object.fromEntries(Object.entries(value).map(([key, member]) => [key, cloneJson(member)]));
}

function integerSeconds(value: JsonValue | undefined): bigint {
  if (value === undefined) return 0n;
  if (typeof value === "number") {
    if (!Number.isSafeInteger(value)) throw new TypeError("seconds must be a safe integer");
    return BigInt(value);
  }
  if (typeof value !== "string" || value.length > 13 || !decimalInteger.test(value)) {
    throw new TypeError("seconds must be a canonical decimal integer");
  }
  return BigInt(value);
}

function integerNanos(value: JsonValue | undefined): number {
  if (value === undefined) return 0;
  if (typeof value !== "number" || !Number.isInteger(value)) {
    throw new TypeError("nanos must be an integer number");
  }
  if (value < -999_999_999 || value > 999_999_999) {
    throw new RangeError("nanos are outside the protobuf range");
  }
  return value;
}

function normalizeWellKnownObject(typeName: string, value: JsonValue): JsonValue {
  if (value === null || typeof value === "string") return value;
  if (!isRecord(value)) return cloneJson(value);
  if (Object.keys(value).some((key) => key !== "seconds" && key !== "nanos")) {
    throw new TypeError(`${typeName} contains an unknown member`);
  }

  const seconds = integerSeconds(value.seconds);
  const nanos = integerNanos(value.nanos);
  if (typeName === timestampTypeName) {
    if (nanos < 0) throw new RangeError("Timestamp nanos must not be negative");
    return toJson(TimestampSchema, create(TimestampSchema, { nanos, seconds }));
  }
  return toJson(DurationSchema, create(DurationSchema, { nanos, seconds }));
}

function normalizeField(field: DescField, value: JsonValue): JsonValue {
  switch (field.fieldKind) {
    case "message":
      return normalizeHttpWktJson(field.message, value);
    case "list":
      if (!Array.isArray(value)) return cloneJson(value);
      if (field.listKind !== "message") return value.map(cloneJson);
      return value.map((member) => normalizeHttpWktJson(field.message, member));
    case "map":
      if (!isRecord(value)) return cloneJson(value);
      if (field.mapKind !== "message") return cloneJson(value);
      return Object.fromEntries(
        Object.entries(value).map(([key, member]) => [
          key,
          normalizeHttpWktJson(field.message, member),
        ]),
      );
    default:
      return cloneJson(value);
  }
}

/** Normalizes daemon stdlib JSON into protobuf-es JSON without mutating the wire value. */
export function normalizeHttpWktJson(descriptor: DescMessage, value: JsonValue): JsonValue {
  if (descriptor.typeName === timestampTypeName || descriptor.typeName === durationTypeName) {
    return normalizeWellKnownObject(descriptor.typeName, value);
  }
  if (!isRecord(value)) return cloneJson(value);

  const normalized = Object.fromEntries(
    Object.entries(value).map(([key, member]) => [key, cloneJson(member)]),
  ) as JsonRecord;
  for (const field of descriptor.fields) {
    for (const key of new Set([field.name, field.jsonName])) {
      if (key !== "" && Object.hasOwn(value, key)) {
        normalized[key] = normalizeField(field, value[key] as JsonValue);
      }
    }
  }
  return normalized;
}
