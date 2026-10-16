// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ShortcutProvider, useShortcut } from "./shortcut-provider";

afterEach(cleanup);

function Probe({ enabled, handler }: { enabled?: boolean; handler: () => void }) {
  useShortcut("schedules.filter", handler, enabled);
  return null;
}

function press(key: string) {
  // fireEvent returns false when a listener called preventDefault.
  return !fireEvent.keyDown(document.body, { key });
}

describe("useShortcut", () => {
  it("claims the key and runs the handler while enabled", () => {
    const handler = vi.fn();
    render(
      <ShortcutProvider>
        <Probe handler={handler} />
      </ShortcutProvider>,
    );
    expect(press("/")).toBe(true);
    expect(handler).toHaveBeenCalledOnce();
  });

  it("leaves the key unclaimed while disabled, and claims it once enabled", () => {
    const handler = vi.fn();
    const { rerender } = render(
      <ShortcutProvider>
        <Probe enabled={false} handler={handler} />
      </ShortcutProvider>,
    );
    expect(press("/")).toBe(false);
    expect(handler).not.toHaveBeenCalled();

    rerender(
      <ShortcutProvider>
        <Probe enabled handler={handler} />
      </ShortcutProvider>,
    );
    expect(press("/")).toBe(true);
    expect(handler).toHaveBeenCalledOnce();
  });
});
