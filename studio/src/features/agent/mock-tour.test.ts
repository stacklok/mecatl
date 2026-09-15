import { describe, expect, it } from "vitest";
import { previewKind } from "@/app/workspace/chat/_components/file-preview";
import {
  isMockTourSession,
  MOCK_TOUR_MESSAGES,
  MOCK_TOUR_SESSION,
  MOCK_TOUR_SESSION_ID,
} from "./mock-tour";

/**
 * The feature tour exists to demonstrate every preview surface, so every
 * artifact and attachment it carries must resolve to a real renderer — the
 * "No preview available" floor would defeat the tour's purpose.
 */
describe("mock feature tour", () => {
  const artifacts = MOCK_TOUR_MESSAGES.flatMap((m) =>
    m.artifact ? [m.artifact] : [],
  );
  const attachments = MOCK_TOUR_MESSAGES.flatMap((m) => m.attachments ?? []);

  it("every artifact resolves to a renderer", () => {
    expect(artifacts.length).toBeGreaterThan(0);
    for (const artifact of artifacts) {
      expect(
        previewKind(artifact.name, artifact.content, artifact.url),
        `artifact ${artifact.name}`,
      ).not.toBe("none");
    }
  });

  it("every attachment resolves to a renderer", () => {
    expect(attachments.length).toBeGreaterThan(0);
    for (const attachment of attachments) {
      expect(
        previewKind(attachment.name, attachment.content, attachment.url),
        `attachment ${attachment.name}`,
      ).not.toBe("none");
    }
  });

  it("covers the code, markdown, image, and PDF renderers", () => {
    const kinds = new Set(
      [...artifacts, ...attachments].map((f) =>
        previewKind(f.name, f.content, f.url),
      ),
    );
    for (const kind of ["code", "markdown", "image", "pdf"]) {
      expect(kinds, `tour must showcase ${kind}`).toContain(kind);
    }
  });

  it("carries an image attachment with a chip-thumbnail source", () => {
    const image = attachments.find(
      (a) => previewKind(a.name, a.content, a.url) === "image",
    );
    expect(image).toBeDefined();
    expect(image?.url ?? image?.content).toMatch(/^data:image\//);
  });

  it("carries no canned thread — threads are the real feature now", () => {
    expect(
      MOCK_TOUR_MESSAGES.every((m) => (m.replies?.length ?? 0) === 0),
    ).toBe(true);
  });

  it("keeps the embedded PDF tiny", () => {
    const pdf = artifacts.find((a) => a.type === "pdf");
    expect(pdf?.url).toMatch(/^data:application\/pdf;base64,/);
    // ~4KB budget on the decoded bytes (base64 is 4/3 the size).
    const b64 = (pdf?.url ?? "").split(",", 2)[1] ?? "";
    expect((b64.length * 3) / 4).toBeLessThan(4096);
  });

  it("self-gates the session row: no rename/delete capabilities", () => {
    expect(MOCK_TOUR_SESSION.id).toBe(MOCK_TOUR_SESSION_ID);
    expect(MOCK_TOUR_SESSION.canRename).toBeUndefined();
    expect(MOCK_TOUR_SESSION.canDelete).toBeUndefined();
    expect(isMockTourSession(MOCK_TOUR_SESSION_ID)).toBe(true);
    expect(isMockTourSession("some-daemon-session")).toBe(false);
  });
});
