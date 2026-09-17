import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { QueuedMessageStrip } from "./queued-message-strip";

/**
 * The held-messages strip above the composer: pending steers with ONE
 * Retract, a queue header that reads muted while a run is live and loud when
 * the queue is paused (with Send now / Edit all / Clear all), and the
 * per-row Steer / Edit / Delete menu.
 */

const queued = [
  { id: "q1", text: "first follow-up" },
  { id: "q2", text: "second follow-up" },
];

const noop = () => {};

describe("QueuedMessageStrip", () => {
  it("renders nothing when nothing is held", () => {
    const { container } = render(
      <QueuedMessageStrip
        queued={[]}
        isStreaming={false}
        onSteer={noop}
        onEdit={noop}
        onDelete={noop}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the paused header with the reason and wires Send now / Edit all / Clear all", () => {
    const onResume = vi.fn();
    const onEditAll = vi.fn();
    const onClearAll = vi.fn();
    render(
      <QueuedMessageStrip
        queued={queued}
        paused={{ reason: "cancelled" }}
        isStreaming={false}
        onSteer={noop}
        onEdit={noop}
        onDelete={noop}
        onResume={onResume}
        onEditAll={onEditAll}
        onClearAll={onClearAll}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "2 queued · paused: cancelled",
    );
    fireEvent.click(screen.getByRole("button", { name: "Send now" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit all" }));
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    expect(onResume).toHaveBeenCalledTimes(1);
    expect(onEditAll).toHaveBeenCalledTimes(1);
    expect(onClearAll).toHaveBeenCalledTimes(1);
    // Every queued row is still listed under the header.
    expect(screen.getByText("first follow-up")).toBeInTheDocument();
    expect(screen.getByText("second follow-up")).toBeInTheDocument();
  });

  it("shows the muted running header (count + ↑ edit hint) while a run is live", () => {
    render(
      <QueuedMessageStrip
        queued={queued}
        isStreaming
        onSteer={noop}
        onEdit={noop}
        onDelete={noop}
        onResume={noop}
        onClearAll={noop}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("2 queued · ↑ edit");
    expect(screen.queryByText(/paused/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Send now" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear all" })).toBeNull();
  });

  it("lists pending steers as steering rows with exactly one Retract for the bundle", () => {
    const onRetractSteers = vi.fn();
    render(
      <QueuedMessageStrip
        queued={[]}
        pendingSteers={[
          { id: "s1", text: "focus on tests" },
          { id: "s2", text: "then the docs" },
        ]}
        isStreaming
        onSteer={noop}
        onEdit={noop}
        onDelete={noop}
        onRetractSteers={onRetractSteers}
      />,
    );
    expect(screen.getAllByTestId("pending-steer-row")).toHaveLength(2);
    expect(screen.getAllByText("steering…")).toHaveLength(2);
    expect(screen.getByText("focus on tests")).toBeInTheDocument();
    const retract = screen.getAllByRole("button", {
      name: "Retract pending steers",
    });
    expect(retract).toHaveLength(1);
    fireEvent.click(retract[0]);
    expect(onRetractSteers).toHaveBeenCalledTimes(1);
    // No queue → no queue header.
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("labels a files-only row by its attachment count", () => {
    const file = new File(["x"], "shot.png", { type: "image/png" });
    render(
      <QueuedMessageStrip
        queued={[{ id: "q1", text: "", files: [file] }]}
        isStreaming={false}
        onSteer={noop}
        onEdit={noop}
        onDelete={noop}
      />,
    );
    expect(screen.getByText("1 attachment")).toBeInTheDocument();
  });

  it("offers Steer on a queued row only while a run is live", () => {
    const onSteer = vi.fn();
    const onEdit = vi.fn();
    const { rerender } = render(
      <QueuedMessageStrip
        queued={[queued[0]]}
        isStreaming
        onSteer={onSteer}
        onEdit={onEdit}
        onDelete={noop}
      />,
    );
    const openMenu = () => {
      const trigger = screen.getByRole("button", {
        name: "Actions for queued message",
      });
      fireEvent.keyDown(trigger, { key: "Enter" });
    };
    openMenu();
    expect(screen.getByRole("menuitem", { name: "Steer" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    expect(onEdit).toHaveBeenCalledWith("q1");

    rerender(
      <QueuedMessageStrip
        queued={[queued[0]]}
        isStreaming={false}
        onSteer={onSteer}
        onEdit={onEdit}
        onDelete={noop}
      />,
    );
    openMenu();
    expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
    expect(
      screen.getByRole("menuitem", { name: "Delete" }),
    ).toBeInTheDocument();
  });

  // Steering off — the daemon does not support it, or the user chose "Queue
  // only" (mecatui --no-steer) — is expressed as an ABSENT onSteer: the row
  // menu keeps Edit / Delete and offers no Steer even while a run is live.
  it("withdraws Steer when onSteer is absent, keeping Edit and Delete", () => {
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    render(
      <QueuedMessageStrip
        queued={[queued[0]]}
        isStreaming
        onEdit={onEdit}
        onDelete={onDelete}
      />,
    );
    fireEvent.keyDown(
      screen.getByRole("button", { name: "Actions for queued message" }),
      { key: "Enter" },
    );
    expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Edit" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(onDelete).toHaveBeenCalledWith("q1");
  });
});
