import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessSoul } from "@/lib/harness/soul";
import { SOUL_NONE, SOUL_UNSUPPORTED, SoulDialog } from "./soul-dialog";

/**
 * The `/soul` built-in's dialog: reads the daemon's resolved soul when it
 * opens and shows the clean body as plain text with its provenance, trust,
 * drift and size; says plainly when no soul is applied or the daemon lacks
 * the route; a failed read offers Retry. Never a markdown render — the body
 * may be project-authored.
 */

const harness = vi.hoisted(() => ({ soul: vi.fn() }));
vi.mock("@/lib/harness/soul", () => ({ fetchHarnessSoul: harness.soul }));

const USER_SOUL: HarnessSoul = {
  present: true,
  content: "# Be terse\n\nAnswer in one line.",
  sizeBytes: 31,
  sha256: "deadbeef",
  provenance: "user",
  trusted: true,
  drifted: false,
};

beforeEach(() => {
  harness.soul.mockReset();
});

describe("SoulDialog", () => {
  it("reads the soul on open and shows the body as plain text with its facts", async () => {
    harness.soul.mockResolvedValue(USER_SOUL);
    render(<SoulDialog open onOpenChange={() => {}} />);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Soul")).toBeInTheDocument();
    const body = await screen.findByTestId("soul-content");
    // Plain text: the heading marker survives verbatim, no <h1> is minted.
    expect(body).toHaveTextContent("# Be terse");
    expect(screen.queryByRole("heading", { name: "Be terse" })).toBeNull();
    expect(screen.getByText("User soul")).toBeInTheDocument();
    expect(screen.getByText("trusted")).toBeInTheDocument();
    expect(screen.queryByText("drifted")).toBeNull();
    expect(screen.getByText("31 bytes")).toBeInTheDocument();
    expect(screen.getByText(/sha256 deadbeef/)).toBeInTheDocument();
    expect(harness.soul).toHaveBeenCalledWith(expect.any(AbortSignal));
  });

  it("flags an untrusted, drifted project soul", async () => {
    harness.soul.mockResolvedValue({
      ...USER_SOUL,
      provenance: "project",
      trusted: false,
      drifted: true,
    });
    render(<SoulDialog open onOpenChange={() => {}} />);
    expect(await screen.findByText("Project soul")).toBeInTheDocument();
    expect(screen.getByText("untrusted")).toBeInTheDocument();
    expect(screen.getByText("drifted")).toBeInTheDocument();
  });

  it("says when no soul is applied, and when the daemon does not report one", async () => {
    harness.soul.mockResolvedValue({ ...USER_SOUL, present: false });
    const view = render(<SoulDialog open onOpenChange={() => {}} />);
    expect(await screen.findByText(SOUL_NONE)).toBeInTheDocument();
    expect(screen.queryByTestId("soul-content")).toBeNull();
    view.unmount();

    harness.soul.mockResolvedValue(null);
    render(<SoulDialog open onOpenChange={() => {}} />);
    expect(await screen.findByText(SOUL_UNSUPPORTED)).toBeInTheDocument();
  });

  it("shows a failed read with Retry, which re-reads", async () => {
    harness.soul
      .mockRejectedValueOnce(new Error("Mecatl is unreachable."))
      .mockResolvedValueOnce(USER_SOUL);
    render(<SoulDialog open onOpenChange={() => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Mecatl is unreachable.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByTestId("soul-content")).toBeInTheDocument();
    await waitFor(() => expect(harness.soul).toHaveBeenCalledTimes(2));
  });

  it("reads nothing while closed", () => {
    render(<SoulDialog open={false} onOpenChange={() => {}} />);
    expect(harness.soul).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
