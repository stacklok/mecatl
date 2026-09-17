/**
 * Unix seconds off an SDK `google.protobuf.Timestamp` (`seconds` is a
 * bigint), 0 when absent — the number shape the UI's date formatting reads.
 * Typed structurally so Studio needs no direct `@bufbuild/protobuf` import.
 */
export function timestampUnix(
  timestamp: { seconds: bigint | number } | undefined,
): number {
  return timestamp ? Number(timestamp.seconds) : 0;
}
