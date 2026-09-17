import { fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { InputSearch } from "./input-search";

/**
 * Pins the props a page needs to drive the search field from outside: a ref
 * to the real input (so a shortcut can focus it), a keydown hook (so it can
 * implement its own Escape), and an accessible name beyond the placeholder.
 */
describe("InputSearch", () => {
  it("hands the underlying input to inputRef and names it via aria-label", () => {
    const ref = createRef<HTMLInputElement>();
    render(
      <InputSearch
        value=""
        onChange={() => {}}
        placeholder="Filter"
        aria-label="Filter things"
        inputRef={ref}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Filter things" });
    expect(ref.current).toBe(input);
    expect(input).toHaveAttribute("placeholder", "Filter");
  });

  it("forwards keydown events from the input", () => {
    const onKeyDown = vi.fn();
    render(
      <InputSearch value="abc" onChange={() => {}} onKeyDown={onKeyDown} />,
    );
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Escape" });
    expect(onKeyDown).toHaveBeenCalledTimes(1);
    expect(onKeyDown.mock.calls[0][0].key).toBe("Escape");
  });

  it("clears through onChange from the clear button", () => {
    const onChange = vi.fn();
    render(<InputSearch value="abc" onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(onChange).toHaveBeenCalledWith("");
  });
});
