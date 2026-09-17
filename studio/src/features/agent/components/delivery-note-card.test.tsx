import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DeliveryNoteInfo } from "@/lib/protocol/delivery-note";
import {
  DeliveryNoteCard,
  deliveryBadge,
  scheduleHref,
} from "./delivery-note-card";

const completed: DeliveryNoteInfo = {
  scheduleName: "nightly digest",
  fireId: "sched--nightly-1",
  kind: "completed",
  stop: "end_turn",
};

describe("DeliveryNoteCard", () => {
  it("links the schedule name to its detail page and labels the fire", () => {
    render(<DeliveryNoteCard delivery={completed} body="Digest: 3 PRs." />);
    const link = screen.getByRole("link", { name: "nightly digest" });
    expect(link).toHaveAttribute(
      "href",
      "/workspace/schedules/nightly%20digest",
    );
    expect(screen.getByText("Scheduled task")).toBeInTheDocument();
    expect(screen.getByText("fire sched--nightly-1")).toBeInTheDocument();
    expect(
      screen.getByRole("article", { name: "Scheduled task nightly digest" }),
    ).toBeInTheDocument();
  });

  it("shows 'completed' for end_turn and 'failed' for error", () => {
    const { rerender } = render(
      <DeliveryNoteCard delivery={completed} body="ok" />,
    );
    expect(screen.getByText("completed")).toBeInTheDocument();
    rerender(
      <DeliveryNoteCard delivery={{ ...completed, stop: "error" }} body="x" />,
    );
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(screen.queryByText("completed")).toBeNull();
  });

  it("shows any other stop reason verbatim, and 'started' for a start note", () => {
    const { rerender } = render(
      <DeliveryNoteCard
        delivery={{ ...completed, stop: "max_turns" }}
        body="partial"
      />,
    );
    expect(screen.getByText("max_turns")).toBeInTheDocument();
    rerender(
      <DeliveryNoteCard
        delivery={{
          scheduleName: "nightly digest",
          fireId: "f-2",
          kind: "started",
        }}
        body=""
      />,
    );
    expect(screen.getByText("started")).toBeInTheDocument();
  });

  it("renders the body as plain text, never as markdown or with the fence", () => {
    render(
      <DeliveryNoteCard
        delivery={completed}
        body={"# heading\n\n**bold** and <<<UNTRUSTED inside the text"}
      />,
    );
    expect(screen.queryByRole("heading")).toBeNull();
    expect(
      screen.getByText(/# heading/, { exact: false }).textContent,
    ).toContain("**bold**");
    // The parser strips the machine markers; a literal one INSIDE the body is
    // the fire's own text and stays — but the card never adds one.
    expect(screen.queryByText("<<<UNTRUSTED")).toBeNull();
  });

  it("omits the body for a start note", () => {
    render(
      <DeliveryNoteCard
        delivery={{ scheduleName: "n", fireId: "f", kind: "started" }}
        body="should not render"
      />,
    );
    expect(screen.queryByText("should not render")).toBeNull();
  });

  it("clamps a long body behind Show more / Show less", () => {
    const body = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join(
      "\n",
    );
    render(<DeliveryNoteCard delivery={completed} body={body} />);
    const toggle = screen.getByRole("button", { name: "Show more" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(screen.getByRole("button", { name: "Show less" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("offers no toggle for a short body", () => {
    render(<DeliveryNoteCard delivery={completed} body="(no output)" />);
    expect(screen.getByText("(no output)")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("deliveryBadge / scheduleHref", () => {
  it("maps stop reasons to labels and variants", () => {
    expect(deliveryBadge(completed)).toEqual({
      label: "completed",
      variant: "success",
    });
    expect(deliveryBadge({ ...completed, stop: "" })).toEqual({
      label: "completed",
      variant: "success",
    });
    expect(deliveryBadge({ ...completed, stop: "error" })).toEqual({
      label: "failed",
      variant: "destructive",
    });
    expect(deliveryBadge({ ...completed, stop: "budget" })).toEqual({
      label: "budget",
      variant: "secondary",
    });
    expect(
      deliveryBadge({ scheduleName: "n", fireId: "f", kind: "started" }),
    ).toEqual({ label: "started", variant: "secondary" });
  });

  it("encodes the schedule name as the route segment", () => {
    expect(scheduleHref("a/b c")).toBe("/workspace/schedules/a%2Fb%20c");
  });
});
