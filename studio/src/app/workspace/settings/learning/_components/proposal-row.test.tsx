import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LearningProposal } from "@/lib/harness/learning";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import { ProposalRow } from "./proposal-row";

/**
 * One review-list row: Approve is offered for staged AND deferred proposals
 * but enabled only when every evidence handle still resolves, with the
 * reason otherwise; Reject is staged-only; the Details disclosure shows
 * where the suggestion came from (a link to each source chat, whether it is
 * still available, the redacted excerpt) and the learned-skill link — none
 * of the technical provenance (sequence numbers, call ids, digests,
 * versions, receipts) — and re-reads the proposal on first open so a
 * version that moved underneath the list is called out.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const evidence = (
  overrides: Partial<LearningProposal["evidence"][number]> = {},
): LearningProposal["evidence"][number] => ({
  sessionId: "session-1",
  locator: "tool",
  ordinal: 3,
  eventSeq: 42,
  toolCallId: "call-9",
  digest: "sha256:abcdef0123456789",
  available: true,
  availability: "",
  preview: "$ task test\nok",
  ...overrides,
});

const proposal = (
  overrides: Partial<LearningProposal> = {},
): LearningProposal => ({
  id: "prop-1",
  version: "2",
  status: "staged",
  kind: "fact",
  key: "deploy/steps",
  value: "Deploy with task release",
  description: "",
  title: "Deploy steps",
  body: "",
  triggers: [],
  evidence: [evidence()],
  evidenceCount: 1,
  decisions: [],
  promotion: null,
  createdAtUnix: 0,
  updatedAtUnix: 0,
  projectScoped: false,
  promotionAvailable: true,
  promotionUnavailableReason: "",
  learnedSkillId: "",
  ...overrides,
});

const noop = () => {};

function renderRow(
  p: LearningProposal,
  handlers: Partial<{
    onApprove: () => void;
    onReject: () => void;
    onUndo: () => void;
    onReplace: (updated: LearningProposal) => void;
  }> = {},
) {
  return render(
    <ul>
      <ProposalRow
        proposal={p}
        busy={false}
        onApprove={handlers.onApprove ?? noop}
        onReject={handlers.onReject ?? noop}
        onUndo={handlers.onUndo ?? noop}
        onReplace={handlers.onReplace ?? noop}
      />
    </ul>,
  );
}

describe("ProposalRow actions", () => {
  it("offers Approve and Reject for a staged proposal whose evidence resolves", async () => {
    const onApprove = vi.fn();
    renderRow(proposal(), { onApprove });
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeEnabled();
    expect(approve).not.toHaveAttribute("title");
    expect(screen.getByRole("button", { name: "Reject" })).toBeEnabled();
    await userEvent.setup().click(approve);
    expect(onApprove).toHaveBeenCalledTimes(1);
  });

  it("offers Approve (not Reject) for a deferred procedure whose evidence resolves, labelled plainly", () => {
    renderRow(proposal({ status: "deferred_unsupported", kind: "procedure" }));
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
    expect(screen.getByText("Deferred")).toBeInTheDocument();
    expect(screen.queryByText("deferred unsupported")).toBeNull();
  });

  it("disables Approve with the reason when any evidence is unavailable", () => {
    renderRow(
      proposal({
        status: "deferred_unsupported",
        evidence: [
          evidence(),
          evidence({ available: false, availability: "digest mismatch" }),
        ],
        evidenceCount: 2,
      }),
    );
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeDisabled();
    expect(approve).toHaveAttribute(
      "title",
      "Some of this proposal's evidence is no longer available or has changed.",
    );
    // The reason is also readable inline, not only on hover.
    expect(
      screen.getByText(
        "Some of this proposal's evidence is no longer available or has changed.",
      ),
    ).toBeInTheDocument();
  });

  it("disables Approve when the proposal records no evidence", () => {
    renderRow(proposal({ evidence: [], evidenceCount: 0 }));
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeDisabled();
    expect(approve).toHaveAttribute(
      "title",
      "This proposal records no evidence to verify.",
    );
  });

  it("prefers the agent's promotion reason", () => {
    renderRow(
      proposal({
        promotionAvailable: false,
        promotionUnavailableReason: "memory target is read-only",
      }),
    );
    expect(screen.getByRole("button", { name: "Approve" })).toHaveAttribute(
      "title",
      "memory target is read-only",
    );
  });

  it("offers Undo approval for a promoted proposal and neither decision", () => {
    renderRow(proposal({ status: "promoted" }));
    expect(screen.getByRole("button", { name: "Undo approval" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
  });

  it("shows the suggestion text without the raw key or trigger tags", () => {
    renderRow(proposal({ triggers: ["deploy", "release"] }));
    expect(screen.getByText("Deploy steps")).toBeInTheDocument();
    expect(screen.getByText("Deploy with task release")).toBeInTheDocument();
    expect(screen.queryByText("deploy/steps")).toBeNull();
    expect(screen.queryByText("deploy")).toBeNull();
    expect(screen.queryByText(/evidence ref/)).toBeNull();
  });
});

describe("ProposalRow details", () => {
  it("reveals the source chats, their availability and the learned-skill link, re-reading the proposal once", async () => {
    const stub = stubHarnessFetch((request) => {
      if (request.path === "/v1/learning/proposals/prop-1") {
        return { proposal: { id: "prop-1", version: "2", status: "promoted" } };
      }
      return undefined;
    });
    const user = userEvent.setup();
    renderRow(
      proposal({
        status: "promoted",
        evidence: [
          evidence(),
          evidence({
            sessionId: "session-2",
            locator: "message",
            ordinal: 7,
            eventSeq: 0,
            toolCallId: "",
            digest: "",
            available: false,
            availability: "source session deleted",
            preview: "",
          }),
        ],
        evidenceCount: 2,
        decisions: [
          {
            kind: "approve",
            actor: "operator",
            reason: "looks right",
            atUnix: Math.floor(Date.now() / 1000) - 120,
          },
        ],
        promotion: {
          memoryKey: "deploy/steps",
          previousExists: true,
          previousVersion: "7",
          resultVersion: "8",
        },
        learnedSkillId: "learned-42",
      }),
    );
    const toggle = screen.getByRole("button", { name: "Details" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("proposal-details")).toBeNull();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const details = screen.getByTestId("proposal-details");
    expect(toggle).toHaveAttribute("aria-controls", details.id);

    // Sources: a link to each chat, availability, and the excerpt.
    expect(
      within(details).getByText("Where this came from"),
    ).toBeInTheDocument();
    const links = within(details).getAllByRole("link", { name: "View chat" });
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/workspace/chat/session-1",
      "/workspace/chat/session-2",
    ]);
    expect(within(details).getByText("Available")).toBeInTheDocument();
    expect(
      within(details).getByText("Unavailable (source session deleted)"),
    ).toBeInTheDocument();
    expect(within(details).getByText(/\$ task test/)).toBeInTheDocument();

    // The technical provenance stays out of the UI.
    expect(within(details).queryByText(/seq 42/)).toBeNull();
    expect(within(details).queryByText(/call-9/)).toBeNull();
    expect(within(details).queryByText(/sha256/)).toBeNull();
    expect(within(details).queryByText(/tool:3/)).toBeNull();
    expect(within(details).queryByText("session-1")).toBeNull();
    expect(within(details).queryByText("Version")).toBeNull();
    expect(within(details).queryByText("Promotion receipt")).toBeNull();
    expect(within(details).queryByText(/replaced version/)).toBeNull();
    expect(within(details).queryByText("Decisions")).toBeNull();
    expect(within(details).queryByText("· operator")).toBeNull();
    expect(within(details).queryByText("learned-42")).toBeNull();

    // The learned skill it became.
    expect(
      within(details).getByRole("link", { name: "View learned skill" }),
    ).toHaveAttribute("href", "/workspace/skills?view=learned");

    // The first open re-read the proposal through the SDK, operator scope.
    await waitFor(() => expect(stub.requests).toHaveLength(1));
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/learning/proposals/prop-1",
    });
    // Same version: nothing to say. Closing and re-opening does not re-read.
    expect(screen.queryByText(/changed since the list was loaded/)).toBeNull();
    await user.click(toggle);
    await user.click(toggle);
    expect(stub.requests).toHaveLength(1);
  });

  it("calls out a version that moved and hands the current proposal back to the list", async () => {
    stubHarnessFetch(() => ({
      proposal: {
        id: "prop-1",
        version: "3",
        status: "rejected",
        evidence: [{ session_id: "session-1", available: true }],
      },
    }));
    const onReplace = vi.fn();
    renderRow(proposal(), { onReplace });
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Details" }));
    expect(
      await screen.findByText(
        "This suggestion changed since the list was loaded. Review it again before deciding.",
      ),
    ).toBeInTheDocument();
    expect(onReplace).toHaveBeenCalledTimes(1);
    expect(onReplace.mock.calls[0]?.[0]).toMatchObject({
      id: "prop-1",
      version: "3",
      status: "rejected",
    });
  });

  it("reports a failed re-read and keeps showing the list's sources", async () => {
    stubHarnessFetch(() =>
      problemResponse(404, "not_found", "proposal is gone"),
    );
    renderRow(proposal());
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Details" }));
    expect(
      await screen.findByText(
        /Could not refresh this suggestion: .*proposal is gone/,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View chat" })).toHaveAttribute(
      "href",
      "/workspace/chat/session-1",
    );
  });

  it("says when there is no source at all", async () => {
    stubHarnessFetch(() =>
      jsonResponse(200, { proposal: { id: "prop-1", version: "2" } }),
    );
    renderRow(proposal({ evidence: [], evidenceCount: 0 }));
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Details" }));
    expect(
      screen.getByText(
        "No source recorded, so this suggestion can’t be approved.",
      ),
    ).toBeInTheDocument();
  });
});
