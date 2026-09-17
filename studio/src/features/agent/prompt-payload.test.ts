import { describe, expect, it, vi } from "vitest";
import type { PromptPart } from "@/lib/harness/client";
import { buildPromptPayload } from "./prompt-payload";

/**
 * The send/steer payload (TUI attach parity): text files inline into the
 * wire text as delimited blocks and become previewable content chips, audio
 * becomes an audio part with a data: URL chip, images go through the
 * injected encoder, and a refusal abandons the whole send with its reason.
 */

const BOTH = { image: true, audio: true };
const encodeImage = vi.fn(
  async (file: File): Promise<PromptPart> => ({
    kind: "image",
    mime_type: file.type,
    data: "aW1n",
  }),
);

describe("buildPromptPayload", () => {
  it("passes a file-less prompt through untouched", async () => {
    await expect(
      buildPromptPayload({
        text: "hi",
        files: [],
        capabilities: BOTH,
        encodeImage,
      }),
    ).resolves.toEqual({
      ok: true,
      text: "hi",
      parts: [],
      attachments: undefined,
    });
  });

  it("inlines text files into the wire text and previews them as content chips", async () => {
    const notes = new File(["alpha\nbeta"], "notes.txt", {
      type: "text/plain",
    });
    const script = new File(["echo hi"], "run.sh", { type: "" });
    const result = await buildPromptPayload({
      text: "Review",
      files: [notes, script],
      capabilities: { image: false, audio: false },
      encodeImage,
    });
    expect(result).toEqual({
      ok: true,
      text: "Review\n\n--- attached file: notes.txt ---\nalpha\nbeta\n--- end of notes.txt ---\n\n--- attached file: run.sh ---\necho hi\n--- end of run.sh ---",
      parts: [],
      attachments: [
        { name: "notes.txt", type: "text/plain", content: "alpha\nbeta" },
        { name: "run.sh", type: "text/plain", content: "echo hi" },
      ],
    });
  });

  it("turns audio into an audio part and a data: URL chip", async () => {
    const clip = new File([new Uint8Array([1, 2, 3])], "clip.wav", {
      type: "audio/wav",
    });
    const result = await buildPromptPayload({
      text: "listen",
      files: [clip],
      capabilities: BOTH,
      encodeImage,
    });
    expect(result).toEqual({
      ok: true,
      text: "listen",
      parts: [{ kind: "audio", mime_type: "audio/wav", data: "AQID" }],
      attachments: [
        {
          name: "clip.wav",
          type: "audio/wav",
          url: "data:audio/wav;base64,AQID",
        },
      ],
    });
  });

  it("encodes images through the injected encoder and keeps staging order across kinds", async () => {
    const image = new File(["x"], "shot.png", { type: "image/png" });
    const text = new File(["t"], "a.txt", { type: "text/plain" });
    const result = await buildPromptPayload({
      text: "both",
      files: [text, image],
      capabilities: BOTH,
      encodeImage,
    });
    expect(encodeImage).toHaveBeenCalledWith(image);
    expect(result).toMatchObject({
      ok: true,
      parts: [{ kind: "image", mime_type: "image/png", data: "aW1n" }],
      attachments: [
        { name: "a.txt", content: "t" },
        { name: "shot.png", url: "data:image/png;base64,aW1n" },
      ],
    });
  });

  it("refuses the whole send when the session's modalities reject a file, before any read", async () => {
    const image = new File(["x"], "shot.png", { type: "image/png" });
    const encoder = vi.fn(encodeImage);
    await expect(
      buildPromptPayload({
        text: "see",
        files: [image],
        capabilities: { image: false, audio: true },
        encodeImage: encoder,
      }),
    ).resolves.toEqual({
      ok: false,
      error: "shot.png: this model does not accept image input.",
    });
    expect(encoder).not.toHaveBeenCalled();
  });

  it("refuses a binary text candidate with its reason", async () => {
    const blob = new File([new Uint8Array([0, 1, 2])], "x.bin", {
      type: "application/octet-stream",
    });
    await expect(
      buildPromptPayload({
        text: "p",
        files: [blob],
        capabilities: BOTH,
        encodeImage,
      }),
    ).resolves.toEqual({
      ok: false,
      error:
        "x.bin: binary files can't be attached — only images, audio and text.",
    });
  });

  it("surfaces an encoder failure as the send's error", async () => {
    const image = new File(["x"], "weird.heic", { type: "image/heic" });
    await expect(
      buildPromptPayload({
        text: "p",
        files: [image],
        capabilities: BOTH,
        encodeImage: async () => {
          throw new Error("weird.heic: this browser cannot decode image/heic");
        },
      }),
    ).resolves.toEqual({
      ok: false,
      error: "weird.heic: this browser cannot decode image/heic",
    });
  });
});
