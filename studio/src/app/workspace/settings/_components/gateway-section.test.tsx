import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GatewaySection } from "./gateway-section";

/**
 * Settings → MCP tools → MCP gateway: the one configure-then-sign-in action
 * in plain words. Pins that (1) the card names itself, says what it is for
 * in one sentence, and offers exactly a name, an address (pre-filled with
 * the suggestion) and a sign-in button that waits for a name; (2) a
 * connected gateway shows as "Connected to" with its name and address;
 * (3) signing in opens the popup first and hands the trimmed name and
 * address to `connectGatewayOAuth`; (4) while the sign-in runs the button
 * says so and is disabled; (5) external mode shows the connection but no
 * form; (6) offline shows no form.
 */

type Runtime = Parameters<typeof GatewaySection>[0]["runtime"];

function runtime(overrides: Record<string, unknown> = {}): Runtime {
  return {
    live: true,
    mode: "managed",
    status: { gateway: null },
    busy: "",
    connectGatewayOAuth: vi.fn(async () => undefined),
    ...overrides,
  } as unknown as Runtime;
}

const CONNECTED = { name: "work-tools", url: "https://gw.example.com/mcp" };

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("GatewaySection", () => {
  it("offers a name, a pre-filled address and a sign-in button that waits for a name", async () => {
    const user = userEvent.setup();
    render(<GatewaySection runtime={runtime()} />);

    expect(
      screen.getByRole("heading", { name: "MCP gateway" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Sign in to a gateway to give the agent its tools."),
    ).toBeInTheDocument();

    const name = screen.getByLabelText("Gateway name");
    expect(name).toHaveValue("");
    expect(screen.getByLabelText("Gateway address")).toHaveValue(
      "https://connector-gateway.stacklok.dev/gw/mcp",
    );
    const signIn = screen.getByRole("button", { name: "Sign in to gateway" });
    expect(signIn).toBeDisabled();
    expect(screen.queryByText("Connected to")).toBeNull();

    await user.type(name, "work-tools");
    expect(signIn).toBeEnabled();
  });

  it("shows the connected gateway by name and address", () => {
    render(
      <GatewaySection runtime={runtime({ status: { gateway: CONNECTED } })} />,
    );
    expect(screen.getByText("Connected to")).toBeInTheDocument();
    expect(screen.getByText("work-tools")).toBeInTheDocument();
    expect(screen.getByText("https://gw.example.com/mcp")).toBeInTheDocument();
  });

  it("opens the sign-in popup and hands the trimmed name and address over", async () => {
    const user = userEvent.setup();
    const popup = { closed: false, location: { href: "" } };
    const open = vi.fn(() => popup);
    vi.stubGlobal("open", open);
    const rt = runtime();
    render(<GatewaySection runtime={rt} />);

    await user.type(screen.getByLabelText("Gateway name"), "  work-tools  ");
    await user.click(
      screen.getByRole("button", { name: "Sign in to gateway" }),
    );

    expect(open).toHaveBeenCalledTimes(1);
    expect(rt.connectGatewayOAuth).toHaveBeenCalledWith(
      "work-tools",
      "https://connector-gateway.stacklok.dev/gw/mcp",
      expect.objectContaining({
        setUrl: expect.any(Function),
        isClosed: expect.any(Function),
      }),
    );
  });

  it("says it is waiting while the sign-in runs", () => {
    render(<GatewaySection runtime={runtime({ busy: "gateway" })} />);
    expect(
      screen.getByRole("button", { name: "Waiting for sign-in…" }),
    ).toBeDisabled();
  });

  it("shows the connection but no form in external mode", () => {
    render(
      <GatewaySection
        runtime={runtime({ mode: "external", status: { gateway: CONNECTED } })}
      />,
    );
    expect(screen.getByText("Connected to")).toBeInTheDocument();
    expect(screen.getByText("work-tools")).toBeInTheDocument();
    expect(screen.queryByLabelText("Gateway name")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("shows no form while the agent is offline", () => {
    render(<GatewaySection runtime={runtime({ live: false })} />);
    expect(screen.getByText(/offline/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Gateway name")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
