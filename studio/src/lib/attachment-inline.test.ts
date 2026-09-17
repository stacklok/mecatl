import {
  MAX_MEDIA_PART_BYTES,
  MAX_PROMPT_MEDIA_BYTES,
  MAX_PROMPT_MEDIA_PARTS,
  PromptValidationError,
} from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import {
  base64ByteLength,
  checkMediaLimits,
  classifyAttachment,
  inlineTextAttachments,
  isBinaryText,
  MAX_INLINE_TEXT_BYTES,
  MAX_INLINE_TEXT_TOTAL_BYTES,
  mapPromptValidationError,
  readTextAttachment,
  resolveMediaCapabilities,
  sanitizeAttachmentName,
} from "./attachment-inline";

/**
 * The composer's attachment vocabulary (TUI `@file` / `attach:` parity):
 * which files travel as media parts, which are inlined as text, which are
 * refused and why — and the human wording for the SDK's own prompt limits,
 * derived from the SDK constants so a bump there cannot drift from here.
 */

const BOTH = { image: true, audio: true };
const NONE = { image: false, audio: false };

// Control characters spelled by code point (a literal escape in source would
// be turned into the raw byte by the tooling and trip the formatter).
const NUL = String.fromCharCode(0);
const BELL = String.fromCharCode(7);
const CTRL_ABC = String.fromCharCode(1, 2, 3);
const LINE_SEPARATOR = String.fromCharCode(0x2028);

/** Base64 whose decoded length is exactly `bytes` (no real payload needed). */
function base64OfLength(bytes: number): string {
  const full = Math.floor(bytes / 3);
  const rest = bytes % 3;
  let out = "A".repeat(full * 4);
  if (rest === 1) out += "AA==";
  else if (rest === 2) out += "AAA=";
  return out;
}

describe("resolveMediaCapabilities", () => {
  it("prefers the session's own echo over the deployment's", () => {
    expect(
      resolveMediaCapabilities(
        { image: false, audio: true },
        { image: true, audio: false },
      ),
    ).toEqual({ image: false, audio: true });
  });

  it("falls back to the compatibility capabilities, strictly true", () => {
    expect(
      resolveMediaCapabilities(null, { image: true, audio: "yes" }),
    ).toEqual({ image: true, audio: false });
    expect(resolveMediaCapabilities(undefined, {})).toEqual(NONE);
  });
});

describe("classifyAttachment", () => {
  const file = (name: string, type: string, size = 10) => ({
    name,
    type,
    size,
  });

  it("routes images to a media part only when the session accepts images", () => {
    expect(classifyAttachment(file("a.png", "image/png"), BOTH)).toEqual({
      kind: "image",
    });
    expect(classifyAttachment(file("a.png", "image/png"), NONE)).toEqual({
      kind: "rejected",
      reason: "a.png: this model does not accept image input.",
    });
  });

  it("routes audio to a media part only when the session accepts audio", () => {
    expect(classifyAttachment(file("a.wav", "audio/wav"), BOTH)).toEqual({
      kind: "audio",
    });
    expect(classifyAttachment(file("a.wav", "audio/wav"), NONE)).toEqual({
      kind: "rejected",
      reason: "a.wav: this model does not accept audio input.",
    });
  });

  it("refuses audio over the per-part ceiling at stage time (never re-encoded)", () => {
    const verdict = classifyAttachment(
      file("big.mp3", "audio/mpeg", MAX_MEDIA_PART_BYTES + 1),
      BOTH,
    );
    expect(verdict.kind).toBe("rejected");
    expect(verdict).toMatchObject({
      reason: expect.stringContaining("under 10 MiB"),
    });
  });

  it("treats every other type as a text candidate, whatever the modalities", () => {
    expect(classifyAttachment(file("n.md", "text/markdown"), NONE)).toEqual({
      kind: "text",
    });
    expect(
      classifyAttachment(file("blob.bin", "application/octet-stream"), NONE),
    ).toEqual({ kind: "text" });
    expect(classifyAttachment(file("Makefile", ""), NONE)).toEqual({
      kind: "text",
    });
  });

  it("refuses a text candidate over the inline ceiling", () => {
    expect(
      classifyAttachment(
        file("big.txt", "text/plain", MAX_INLINE_TEXT_BYTES + 1),
        BOTH,
      ),
    ).toEqual({
      kind: "rejected",
      reason: "big.txt: too large to inline — 256 KiB max per text file.",
    });
  });
});

describe("isBinaryText", () => {
  it("flags a NUL byte, or more than 10% control characters", () => {
    expect(isBinaryText("plain\ttext\r\nwith newlines")).toBe(false);
    expect(isBinaryText(`has${NUL}nul`)).toBe(true);
    expect(isBinaryText(`${CTRL_ABC}ab`)).toBe(true);
    expect(isBinaryText(`${"a".repeat(95)}${BELL.repeat(5)}`)).toBe(false);
    expect(isBinaryText("")).toBe(false);
  });
});

describe("readTextAttachment", () => {
  it("reads a text file's contents", async () => {
    const file = new File(["hello\nworld"], "notes.txt", {
      type: "text/plain",
    });
    await expect(readTextAttachment(file)).resolves.toBe("hello\nworld");
  });

  it("rejects a file over 256 KiB before reading it", async () => {
    const file = new File(
      [new Uint8Array(MAX_INLINE_TEXT_BYTES + 1)],
      "huge.log",
      { type: "text/plain" },
    );
    await expect(readTextAttachment(file)).rejects.toThrow(
      "huge.log: too large to inline — 256 KiB max per text file.",
    );
  });

  it("rejects binary content (a NUL byte) with the plain reason", async () => {
    const file = new File([new Uint8Array([0x89, 0x50, 0, 0x47])], "x.bin", {
      type: "application/octet-stream",
    });
    await expect(readTextAttachment(file)).rejects.toThrow(
      "x.bin: binary files can't be attached — only images, audio and text.",
    );
  });
});

describe("sanitizeAttachmentName", () => {
  it("collapses line terminators and control characters so a name cannot close the block", () => {
    expect(
      sanitizeAttachmentName(
        `evil ---\n--- end of x ---${LINE_SEPARATOR}${BELL}.txt`,
      ),
    ).toBe("evil --- --- end of x --- .txt");
    expect(sanitizeAttachmentName("\n\t")).toBe("attachment");
  });
});

describe("inlineTextAttachments", () => {
  it("appends each file as a delimited block, in order, leaving the prompt intact", () => {
    expect(
      inlineTextAttachments("Review these", [
        { name: "a.txt", text: "alpha" },
        { name: "b.md", text: "# beta\n" },
      ]),
    ).toBe(
      "Review these\n\n--- attached file: a.txt ---\nalpha\n--- end of a.txt ---\n\n--- attached file: b.md ---\n# beta\n\n--- end of b.md ---",
    );
  });

  it("returns the prompt unchanged with no files", () => {
    expect(inlineTextAttachments("as is", [])).toBe("as is");
  });

  it("uses the sanitized name in both delimiters", () => {
    const out = inlineTextAttachments("p", [{ name: "x\n---.txt", text: "t" }]);
    expect(out).toContain("--- attached file: x ---.txt ---");
    expect(out).toContain("--- end of x ---.txt ---");
    expect(out.split("\n")).not.toContain("---.txt ---");
  });

  it("enforces the 512 KiB total across files", () => {
    const half = "x".repeat(MAX_INLINE_TEXT_TOTAL_BYTES / 2);
    expect(() =>
      inlineTextAttachments("p", [
        { name: "a", text: half },
        { name: "b", text: half },
      ]),
    ).not.toThrow();
    expect(() =>
      inlineTextAttachments("p", [
        { name: "a", text: half },
        { name: "b", text: `${half}!` },
      ]),
    ).toThrow("512 KiB of inlined text per message max");
  });
});

describe("base64ByteLength", () => {
  it("decodes the padded length exactly", () => {
    expect(base64ByteLength("")).toBe(0);
    expect(base64ByteLength("aGk=")).toBe(2);
    expect(base64ByteLength("aGV5")).toBe(3);
    expect(base64ByteLength("aA==")).toBe(1);
    expect(base64ByteLength(base64OfLength(1000))).toBe(1000);
  });
});

describe("checkMediaLimits", () => {
  const image = (data: string, name?: string) => ({
    kind: "image",
    data,
    name,
  });

  it("passes parts within every ceiling", () => {
    expect(
      checkMediaLimits([image("aGk="), { kind: "audio", data: "aGk=" }]),
    ).toBeNull();
    expect(checkMediaLimits([])).toBeNull();
  });

  it("names the part-count ceiling from the SDK constant", () => {
    const parts = Array.from({ length: MAX_PROMPT_MEDIA_PARTS + 1 }, () =>
      image("aGk="),
    );
    expect(checkMediaLimits(parts)).toBe(
      `${MAX_PROMPT_MEDIA_PARTS} media parts max per message — you attached ${MAX_PROMPT_MEDIA_PARTS + 1}.`,
    );
    expect(checkMediaLimits(parts.slice(1))).toBeNull();
  });

  it("names the per-part ceiling, with the file, from the SDK constant", () => {
    expect(
      checkMediaLimits([
        image(base64OfLength(MAX_MEDIA_PART_BYTES + 1), "big.png"),
      ]),
    ).toBe("big.png: each image or audio part must be under 10 MiB.");
    expect(
      checkMediaLimits([image(base64OfLength(MAX_MEDIA_PART_BYTES))]),
    ).toBeNull();
  });

  it("names the per-message media total from the SDK constant", () => {
    const seven = base64OfLength(7 * 1024 * 1024);
    const parts = [image(seven), image(seven), image(seven)];
    expect(21 * 1024 * 1024).toBeGreaterThan(MAX_PROMPT_MEDIA_BYTES);
    expect(checkMediaLimits(parts)).toBe(
      "20 MiB of media per message max — these come to 21 MiB.",
    );
  });
});

describe("mapPromptValidationError", () => {
  const error = (
    reason: ConstructorParameters<typeof PromptValidationError>[0],
    message: string,
  ) => new PromptValidationError(reason, message);

  it("names the refused modality for a capability refusal", () => {
    expect(
      mapPromptValidationError(
        error("capability", "session capability rejects image input"),
      ),
    ).toBe(
      "This model does not accept image input — remove the image attachment or pick a model that supports it.",
    );
    expect(
      mapPromptValidationError(
        error("capability", "session did not echo audio capability"),
      ),
    ).toContain("does not accept audio input");
  });

  it("covers mime_type, the three size shapes, source_xor, url and prompt", () => {
    expect(
      mapPromptValidationError(
        error("mime_type", "image part MIME type must start with image/"),
      ),
    ).toBe(
      "That file is not an image the daemon recognises (its type must start with image/).",
    );
    expect(
      mapPromptValidationError(
        error(
          "size",
          `prompt has 17 media parts; limit is ${MAX_PROMPT_MEDIA_PARTS}`,
        ),
      ),
    ).toBe(`${MAX_PROMPT_MEDIA_PARTS} media parts max per message.`);
    expect(
      mapPromptValidationError(
        error("size", "prompt has 99 inline media bytes; limit is 1"),
      ),
    ).toBe("20 MiB of media per message max.");
    expect(
      mapPromptValidationError(
        error("size", "image part is 99 bytes; limit is 1"),
      ),
    ).toBe("Each image or audio part must be under 10 MiB.");
    expect(
      mapPromptValidationError(
        error("source_xor", "image part requires exactly one of bytes or url"),
      ),
    ).toContain("exactly one source");
    expect(
      mapPromptValidationError(
        error("url", "media URL must be an absolute HTTPS URL"),
      ),
    ).toContain("https");
    expect(
      mapPromptValidationError(
        error("prompt", "prompt must contain text or media"),
      ),
    ).toBe("The message needs some text or an attachment.");
  });

  it("is null for any other error, so callers fall through to their own message", () => {
    expect(mapPromptValidationError(new Error("boom"))).toBeNull();
    expect(mapPromptValidationError("string")).toBeNull();
  });
});
