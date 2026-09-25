import type { MessageInitShape } from "@bufbuild/protobuf";

import { normalizeError, PromptValidationError, type TransportKind } from "./errors.js";
import type { UploadArtifactRequestSchema } from "./gen/mecatl/v1/harness_pb.js";

/** The server's PDF upload limits, also enforced before SDK transport writes. */
export const PDF_CHUNK_BYTES = 256 << 10;
export const PDF_MAX_BYTES = 20 << 20;

export type PdfSource = Blob | AsyncIterable<Uint8Array>;

function invalid(reason: "mime_type" | "prompt" | "size", message: string): never {
  throw new PromptValidationError(reason, message);
}

/** Validate the basename before any source bytes or transport frames are consumed. */
export function validatePdfUpload(source: PdfSource, name: string, mimeType: string): void {
  if (mimeType !== "application/pdf") {
    invalid("mime_type", "artifact MIME type must be application/pdf");
  }
  if (
    typeof name !== "string" ||
    name.trim() !== name ||
    name === "" ||
    name === "." ||
    name === ".." ||
    [...name].length > 255 ||
    /[\\/\\:\p{Cc}\p{Cf}]/u.test(name)
  ) {
    invalid("prompt", "PDF name must be a safe basename of 1 to 255 characters");
  }
  if (source instanceof Blob) {
    if (source.type !== "" && source.type.toLowerCase() !== "application/pdf") {
      invalid("mime_type", "PDF Blob type must be application/pdf");
    }
    if (source.size > PDF_MAX_BYTES) {
      invalid("size", `PDF is ${source.size} bytes; limit is ${PDF_MAX_BYTES}`);
    }
  } else if (
    source === null ||
    typeof source !== "object" ||
    typeof source[Symbol.asyncIterator] !== "function"
  ) {
    invalid("prompt", "PDF source must be a Blob or async byte iterable");
  }
}

interface ByteSource {
  readonly iterator: AsyncIterator<Uint8Array>;
  close(finished: boolean): Promise<void>;
}

function byteSource(source: PdfSource): ByteSource {
  if (source instanceof Blob) {
    const reader = source.stream().getReader();
    return {
      iterator: { next: () => reader.read() },
      async close() {
        try {
          await reader.cancel();
        } finally {
          reader.releaseLock();
        }
      },
    };
  }
  const iterator = source[Symbol.asyncIterator]();
  return {
    iterator,
    async close(finished: boolean) {
      if (!finished) await iterator.return?.();
    },
  };
}

function nextWithCancellation(
  iterator: AsyncIterator<Uint8Array>,
  signal: AbortSignal,
  transport: TransportKind,
): Promise<IteratorResult<Uint8Array>> {
  if (signal.aborted) return Promise.reject(normalizeError(signal.reason, transport));
  return new Promise((resolve, reject) => {
    const onAbort = () => reject(normalizeError(signal.reason, transport));
    signal.addEventListener("abort", onAbort, { once: true });
    Promise.resolve()
      .then(() => {
        if (signal.aborted) throw normalizeError(signal.reason, transport);
        return iterator.next();
      })
      .then(resolve, reject)
      .finally(() => signal.removeEventListener("abort", onAbort));
  });
}

/** Produces one metadata frame, then only nonempty bounded chunks on demand. */
export async function* pdfUploadFrames(
  source: PdfSource,
  sessionId: string,
  name: string,
  signal: AbortSignal,
  transport: TransportKind,
  onFailure?: (cause: unknown) => void,
): AsyncIterable<MessageInitShape<typeof UploadArtifactRequestSchema>> {
  try {
    if (signal.aborted) throw normalizeError(signal.reason, transport);
    yield {
      payload: {
        case: "metadata",
        value: { mimeType: "application/pdf", name, sessionId },
      },
    };

    const bytes = byteSource(source);
    let finished = false;
    let total = 0;
    try {
      for (;;) {
        const next = await nextWithCancellation(bytes.iterator, signal, transport);
        if (next.done) {
          finished = true;
          break;
        }
        if (!(next.value instanceof Uint8Array)) {
          invalid("prompt", "PDF source yielded a value that is not Uint8Array");
        }
        if (next.value.byteLength === 0) continue;
        total += next.value.byteLength;
        if (total > PDF_MAX_BYTES) {
          invalid("size", `PDF exceeds ${PDF_MAX_BYTES} bytes`);
        }
        for (let offset = 0; offset < next.value.byteLength; offset += PDF_CHUNK_BYTES) {
          if (signal.aborted) throw normalizeError(signal.reason, transport);
          yield {
            payload: {
              case: "chunk",
              value: next.value.subarray(offset, offset + PDF_CHUNK_BYTES),
            },
          };
        }
      }
      if (total === 0) invalid("prompt", "PDF source contains no bytes");
    } finally {
      // An aborted async generator may still have one pending next(). Do not wait
      // for a caller-owned source that ignores cancellation before rejecting.
      const closing = bytes.close(finished);
      if (signal.aborted) void closing.catch(() => undefined);
      else await closing;
    }
  } catch (cause) {
    onFailure?.(cause);
    throw cause;
  }
}
