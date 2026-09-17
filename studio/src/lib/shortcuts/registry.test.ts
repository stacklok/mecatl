import { describe, expect, it } from "vitest";
import { RESERVED_COMBOS } from "./keymap";
import {
  comboFiresWhileTyping,
  describeShortcut,
  keycaps,
  matchCombo,
  SHORTCUT_GROUPS,
  SHORTCUTS,
} from "./registry";

/** Build a minimal KeyboardEvent-like object for matchCombo. */
function ev(
  key: string,
  mods: Partial<{
    meta: boolean;
    ctrl: boolean;
    shift: boolean;
    alt: boolean;
  }> = {},
): KeyboardEvent {
  return {
    key,
    metaKey: mods.meta ?? false,
    ctrlKey: mods.ctrl ?? false,
    shiftKey: mods.shift ?? false,
    altKey: mods.alt ?? false,
  } as KeyboardEvent;
}

describe("shortcut registry", () => {
  it("has unique ids", () => {
    const ids = SHORTCUTS.map((s) => s.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("every shortcut belongs to a known group", () => {
    for (const s of SHORTCUTS) {
      expect(SHORTCUT_GROUPS).toContain(
        s.group as (typeof SHORTCUT_GROUPS)[number],
      );
    }
  });

  it("pins the app-wide bindings to their combos", () => {
    const byId = new Map(SHORTCUTS.map((s) => [s.id, s.combo]));
    expect(byId.get("search.open")).toBe("mod+k");
    expect(byId.get("settings.open")).toBe("mod+,");
    expect(byId.get("shortcuts.open")).toBe("?");
    expect(byId.get("shortcuts.open.mod")).toBe("mod+/");
    expect(byId.get("chat.toggleList")).toBe("mod+b");
    expect(byId.get("close.esc")).toBe("esc");
    // Deliberately NOT mod+n: browsers reserve ⌘N/Ctrl+N (new window) and the
    // page can't intercept it, so "New chat" stays on the preventable ⌘⇧O.
    expect(byId.get("chat.new")).toBe("mod+shift+o");
    // The Agents panel toggle (the TUI's f6): a preventable ⌘⇧ chord, off the
    // browser-reserved ⌘⇧A/N/T/W.
    expect(byId.get("agents.toggle")).toBe("mod+shift+l");
    expect(SHORTCUTS.find((s) => s.id === "agents.toggle")?.group).toBe(
      "General",
    );
    // The `/session` details dialog: ⌘I is preventable everywhere; ⌘⇧I is
    // DevTools on Windows/Linux and would never reach the page.
    expect(byId.get("chat.details")).toBe("mod+i");
    expect(SHORTCUTS.find((s) => s.id === "chat.details")?.group).toBe("Chats");
    expect(comboFiresWhileTyping("mod+i")).toBe(true);
    // Clear conversation (the TUI's /clear): a preventable ⌘⇧ chord, off the
    // Agents panel's ⌘⇧L and Firefox's uninterceptable ⌘⇧K; a ⌘ chord, so
    // it fires from the composer too.
    expect(byId.get("chat.clear")).toBe("mod+shift+x");
    expect(SHORTCUTS.find((s) => s.id === "chat.clear")?.group).toBe("Chats");
    expect(comboFiresWhileTyping("mod+shift+x")).toBe(true);
    // The Tools group (the TUI's ctrl+o / ctrl+r / f8 MCP overlays): the
    // punctuation keys beside Enter — no free mnemonic letter chord is left
    // that every browser yields — all ⌘ chords, so they fire from the
    // composer too.
    expect(byId.get("mcp.inventory")).toBe("mod+.");
    expect(byId.get("mcp.resources")).toBe("mod+;");
    expect(byId.get("mcp.prompts")).toBe("mod+'");
  });

  it("binds the three MCP overlays (the TUI's ctrl+o, ctrl+r, f8) to preventable ⌘-punctuation chords in a Tools group", () => {
    const tools = SHORTCUTS.filter((s) => s.group === "Tools");
    expect(tools.map((s) => s.id)).toEqual([
      "mcp.inventory",
      "mcp.resources",
      "mcp.prompts",
    ]);
    expect(SHORTCUT_GROUPS).toContain("Tools");
    // The docs page renders groups in order; Tools is the last one.
    expect(SHORTCUT_GROUPS[SHORTCUT_GROUPS.length - 1]).toBe("Tools");
    for (const def of tools) {
      // Dispatched (a component registers a handler), rebindable, and live
      // while typing in the composer.
      expect(def.fixed).toBeUndefined();
      expect(def.locked).toBeUndefined();
      expect(comboFiresWhileTyping(def.combo)).toBe(true);
    }
    expect(
      SHORTCUTS.find((s) => s.id === "mcp.inventory")?.description,
    ).toContain("ctrl+o");
    expect(
      SHORTCUTS.find((s) => s.id === "mcp.resources")?.description,
    ).toContain("ctrl+r");
    expect(
      SHORTCUTS.find((s) => s.id === "mcp.prompts")?.description,
    ).toContain("f8");
    // The chords match by the produced character on either modifier, and a
    // held shift on a symbol is layout noise, not a different chord (a
    // German ";" is ⇧,) — while the bare key never matches.
    expect(matchCombo("mod+.", ev(".", { meta: true }))).toBe(true);
    expect(matchCombo("mod+.", ev(".", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+.", ev("."))).toBe(false);
    expect(matchCombo("mod+;", ev(";", { ctrl: true, shift: true }))).toBe(
      true,
    );
    expect(matchCombo("mod+'", ev("'", { meta: true }))).toBe(true);
    expect(matchCombo("mod+'", ev("Dead", { meta: true }))).toBe(false);
    expect(keycaps("mod+.")).toEqual(["⌘", "."]);
    expect(keycaps("mod+;")).toEqual(["⌘", ";"]);
    expect(keycaps("mod+'")).toEqual(["⌘", "'"]);
  });

  it("binds Debug with AI (the TUI's F1) to a preventable, browser-free chord in the Chats group", () => {
    const def = SHORTCUTS.find((s) => s.id === "debug.open");
    // Not bare F1: the browser's help key (and a Mac laptop's brightness key)
    // — the keymap reserves it. ⌘⇧Y is off every reserved chord.
    expect(def?.combo).toBe("mod+shift+y");
    expect(def?.group).toBe("Chats");
    expect(def?.description).toContain("Debug the selected chat with AI");
    expect(def?.description).toContain("F1");
    // Dispatched (the workspace registers a handler), so NOT documentation-only.
    expect(def?.fixed).toBeUndefined();
    expect(comboFiresWhileTyping("mod+shift+y")).toBe(true);
    expect(
      matchCombo("mod+shift+y", ev("Y", { meta: true, shift: true })),
    ).toBe(true);
    expect(
      matchCombo("mod+shift+y", ev("Y", { ctrl: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+y", ev("y", { ctrl: true }))).toBe(false);
    expect(matchCombo("mod+shift+y", ev("F1"))).toBe(false);
    expect(keycaps("mod+shift+y")).toEqual(["⌘", "⇧", "Y"]);
  });

  it("binds Expand/collapse details (the TUI's ctrl+t) to a preventable ⌘⇧ chord that fires while typing", () => {
    const def = SHORTCUTS.find((s) => s.id === "chat.expandDetails");
    // ⌘⇧G (find previous — pages may claim it everywhere), NOT ⌘⇧E: Firefox's
    // Network Monitor owns Ctrl+Shift+E on Windows/Linux and never yields it.
    expect(def?.combo).toBe("mod+shift+g");
    expect(def?.group).toBe("Conversation");
    expect(def?.fixed).toBeUndefined();
    expect(comboFiresWhileTyping("mod+shift+g")).toBe(true);
    expect(
      matchCombo("mod+shift+g", ev("G", { meta: true, shift: true })),
    ).toBe(true);
    expect(
      matchCombo("mod+shift+g", ev("g", { ctrl: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+g", ev("g", { ctrl: true }))).toBe(false);
  });

  it("gives every live shortcut a combo of its own", () => {
    const live = SHORTCUTS.filter((s) => !s.fixed);
    const combos = live.map((s) => s.combo);
    expect(new Set(combos).size).toBe(combos.length);
  });

  it("binds the per-chat copy-ID and fork keys (the TUI's c / f) in the Chats group, off every browser chord", () => {
    const copyId = SHORTCUTS.find((s) => s.id === "chat.copyId");
    const fork = SHORTCUTS.find((s) => s.id === "chat.fork");
    // Copy ID is a bare letter like the vim-style j/k rows: it never fires
    // while typing. Fork is a ⌘ chord, so it fires from the composer too.
    expect(copyId?.combo).toBe("c");
    expect(fork?.combo).toBe("mod+shift+s");
    for (const def of [copyId, fork]) {
      expect(def?.group).toBe("Chats");
      // Dispatched (the workspace registers handlers), so NOT documentation-only.
      expect(def?.fixed).toBeUndefined();
    }
    expect(copyId?.description).toContain("session ID");
    expect(fork?.description).toContain("Fork");
    expect(comboFiresWhileTyping("c")).toBe(false);
    expect(comboFiresWhileTyping("mod+shift+s")).toBe(true);
    // Neither sits on a chord a browser answers before the page: the
    // DevTools trio (⌘⇧I/J/C), the window/tab lifecycle (⌘⇧N/T/W), Firefox's
    // Downloads (⌘⇧Y — Debug with AI's, which yields, but not ours) and
    // Clear browsing data (⌘⇧Delete). ⌘⇧C — the copy mnemonic — is Inspect
    // Element and is deliberately absent from the whole registry.
    const BROWSER_CHORDS = [
      "mod+shift+i",
      "mod+shift+j",
      "mod+shift+c",
      "mod+shift+n",
      "mod+shift+t",
      "mod+shift+w",
      "mod+shift+y",
      "mod+shift+delete",
    ];
    for (const combo of [copyId?.combo, fork?.combo]) {
      expect(BROWSER_CHORDS).not.toContain(combo);
      expect(RESERVED_COMBOS.has(combo ?? "")).toBe(false);
    }
    expect(SHORTCUTS.some((s) => s.combo === "mod+shift+c")).toBe(false);
    // Live matching: the letter without modifiers only; the chord on either
    // modifier, with shift, and never without it.
    expect(matchCombo("c", ev("c"))).toBe(true);
    expect(matchCombo("c", ev("c", { meta: true }))).toBe(false);
    expect(matchCombo("c", ev("C", { shift: true }))).toBe(false);
    expect(
      matchCombo("mod+shift+s", ev("S", { meta: true, shift: true })),
    ).toBe(true);
    expect(
      matchCombo("mod+shift+s", ev("s", { ctrl: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+s", ev("s", { ctrl: true }))).toBe(false);
    expect(keycaps("mod+shift+s")).toEqual(["⌘", "⇧", "S"]);
    expect(keycaps("c")).toEqual(["C"]);
  });

  it("binds Jump to the most recent chat (the --resume-latest analogue) to a bare l beside j/k", () => {
    const def = SHORTCUTS.find((s) => s.id === "chat.latest");
    expect(def?.combo).toBe("l");
    expect(def?.group).toBe("Chats");
    expect(def?.fixed).toBeUndefined();
    // A bare letter, like the vim-style j/k rows: it keeps typing plain.
    expect(comboFiresWhileTyping("l")).toBe(false);
  });

  it("binds the schedules list filter to a bare slash in its own group", () => {
    const def = SHORTCUTS.find((s) => s.id === "schedules.filter");
    expect(def?.combo).toBe("/");
    expect(def?.group).toBe("Scheduled");
    expect(SHORTCUT_GROUPS).toContain("Scheduled");
    // A bare slash must stay plain text inside the filter (and the composer):
    // the dispatcher suppresses it while typing, so focusing the filter with
    // `/` never inserts a slash into it.
    expect(comboFiresWhileTyping("/")).toBe(false);
  });

  it("pins the transcript paging keys in their own Conversation group", () => {
    const byId = new Map(SHORTCUTS.map((s) => [s.id, s]));
    expect(byId.get("transcript.pageUp")?.combo).toBe("pageup");
    expect(byId.get("transcript.pageDown")?.combo).toBe("pagedown");
    expect(byId.get("transcript.top")?.combo).toBe("shift+pageup");
    expect(byId.get("transcript.bottom")?.combo).toBe("shift+pagedown");
    for (const id of [
      "transcript.pageUp",
      "transcript.pageDown",
      "transcript.top",
      "transcript.bottom",
    ]) {
      expect(byId.get(id)?.group).toBe("Conversation");
    }
    // Rendered between the chat-list keys and the composer keys.
    const groups = [...SHORTCUT_GROUPS];
    expect(groups.indexOf("Conversation")).toBe(groups.indexOf("Chats") + 1);
    expect(groups.indexOf("Composer")).toBe(groups.indexOf("Conversation") + 1);
  });

  it("dispatches ⇧PgUp / ⇧PgDn to the top/bottom jumps (first match wins)", () => {
    // The dispatcher fires the FIRST registry entry that matches, so the
    // bare `pageup` entry must not swallow a shifted press.
    const firstMatch = (e: KeyboardEvent) =>
      SHORTCUTS.find((s) => matchCombo(s.combo, e))?.id;
    expect(firstMatch(ev("PageUp", { shift: true }))).toBe("transcript.top");
    expect(firstMatch(ev("PageDown", { shift: true }))).toBe(
      "transcript.bottom",
    );
    expect(firstMatch(ev("PageUp"))).toBe("transcript.pageUp");
    expect(firstMatch(ev("PageDown"))).toBe("transcript.pageDown");
  });

  it("documents the transcript select-all as a fixed ⌘A in the Conversation group", () => {
    const def = SHORTCUTS.find((s) => s.id === "transcript.selectAll");
    expect(def?.combo).toBe("mod+a");
    expect(def?.group).toBe("Conversation");
    // Fixed: the transcript container owns the keydown, so the chord fires
    // only while the conversation has focus; the dispatcher never claims
    // mod+a (no component registers a handler for this id).
    expect(def?.fixed).toBe(true);
    expect(keycaps("mod+a")).toEqual(["⌘", "A"]);
  });

  it("binds the model + effort picker opener (the TUI's F7) to a preventable, browser-free chord", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.model");
    // Off ⌘⇧M: Chrome's profile switcher / Firefox's responsive-design mode.
    expect(def?.combo).toBe("mod+shift+f");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe("Open the model and effort picker");
    // Dispatched (the picker registers a handler), so NOT documentation-only.
    expect(def?.fixed).toBeUndefined();
    expect(comboFiresWhileTyping("mod+shift+f")).toBe(true);
    expect(
      matchCombo("mod+shift+f", ev("F", { meta: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+f", ev("f", { ctrl: true }))).toBe(false);
  });

  it("marks every composer binding fixed (component-owned, documentation-only)", () => {
    // The picker opener is the one dispatched composer shortcut.
    const composer = SHORTCUTS.filter(
      (s) => s.id.startsWith("composer.") && s.id !== "composer.model",
    );
    expect(composer.length).toBeGreaterThan(0);
    for (const def of composer) expect(def.fixed).toBe(true);
    // The dispatched bindings are NOT fixed — they have handlers.
    for (const id of ["close.esc", "search.open", "transcript.pageUp"]) {
      expect(SHORTCUTS.find((s) => s.id === id)?.fixed).toBeUndefined();
    }
  });

  it("documents the `/` palette as Studio built-ins plus the chat's workspace commands", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.slash");
    expect(def?.combo).toBe("/");
    expect(def?.group).toBe("Composer");
    expect(def?.fixed).toBe(true);
    // Both layers are named, and the capability gate is stated so the
    // reference does not promise a row the daemon may hide.
    expect(def?.description).toMatch(/built-ins/);
    expect(def?.description).toMatch(/workspace commands/);
    expect(def?.description).toMatch(/capability-gated/);
    for (const name of ["/clear", "/help", "/session", "/mcp", "/models"]) {
      expect(def?.description).toContain(name);
    }
  });

  it("documents the permission-mode cycle as a fixed ⇧Tab composer row (the TUI's shift+tab)", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.mode.cycle");
    expect(def?.combo).toBe("shift+tab");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe(
      "Cycle the permission mode — Manual → Plan → Accept edits (mid-run: held until the turn ends)",
    );
    // Fixed, not locked: the composer's own editor keydown owns the chord,
    // so it fires only with the caret in the composer. The global dispatcher
    // never claims ⇧Tab — outside the composer it must stay the browser's
    // reverse-focus key (forms, dialogs, assistive technology).
    expect(def?.fixed).toBe(true);
    expect(def?.locked).toBeUndefined();
    expect(comboFiresWhileTyping("shift+tab")).toBe(false);
    expect(keycaps("shift+tab")).toEqual(["⇧", "Tab"]);
    // The chord is ⇧Tab and only ⇧Tab: a bare Tab, ⌘⇧Tab (the browser's tab
    // switch) and ⌥⇧Tab are different keys.
    expect(matchCombo("shift+tab", ev("Tab", { shift: true }))).toBe(true);
    expect(matchCombo("shift+tab", ev("Tab"))).toBe(false);
    expect(
      matchCombo("shift+tab", ev("Tab", { shift: true, meta: true })),
    ).toBe(false);
    expect(
      matchCombo("shift+tab", ev("Tab", { shift: true, ctrl: true })),
    ).toBe(false);
    expect(matchCombo("shift+tab", ev("Tab", { shift: true, alt: true }))).toBe(
      false,
    );
  });

  it("documents paste as a fixed ⌘V in the Composer group (the TUI's ctrl+v)", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.paste");
    expect(def?.combo).toBe("mod+v");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe(
      "Paste — a clipboard image attaches; a large text paste is staged as [Pasted text #N] and expands on send",
    );
    // Fixed: the browser owns ⌘V and the composer's paste listener decides
    // what the clipboard becomes; the dispatcher never claims the chord (no
    // handler is registered), so paste keeps working everywhere else.
    expect(def?.fixed).toBe(true);
    expect(keycaps("mod+v")).toEqual(["⌘", "V"]);
  });

  it("documents the double-Esc draft clear as a fixed Esc row in the Composer group", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.clearDraft");
    expect(def?.combo).toBe("esc");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe("Press twice on an idle draft to clear it");
    // Fixed: the press rides close.esc and is forwarded to the composer;
    // nothing registers a handler for this id, so it is never dispatched.
    expect(def?.fixed).toBe(true);
    expect(def?.locked).toBeUndefined();
  });

  it("documents the one-shot draft clear (the TUI's ctrl+u) as a fixed ⌘⇧U composer row", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.clearDraft.key");
    // NOT ⌘⇧⌫: on macOS the delete key reports as Backspace, so that chord is
    // Chrome's/Firefox's Clear browsing data (⌘⇧Delete, keymap-reserved).
    expect(def?.combo).toBe("mod+shift+u");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe(
      "Clear the unsent draft — text, staged pastes and attachments (also the × beside Send)",
    );
    // Fixed: the composer's own editor keydown owns the chord (it fires only
    // with the caret in the field); the dispatcher never claims it.
    expect(def?.fixed).toBe(true);
    expect(def?.locked).toBeUndefined();
    expect(keycaps("mod+shift+u")).toEqual(["⌘", "⇧", "U"]);
    expect(
      matchCombo("mod+shift+u", ev("U", { meta: true, shift: true })),
    ).toBe(true);
    expect(
      matchCombo("mod+shift+u", ev("U", { ctrl: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+u", ev("Backspace", { meta: true }))).toBe(
      false,
    );
    expect(matchCombo("mod+shift+u", ev("u", { meta: true }))).toBe(false);
  });

  it("documents the unconditional newline (the TUI's ctrl+j) as a fixed ⌘Enter composer row", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.newline.mod");
    expect(def?.combo).toBe("mod+enter");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toBe(
      "Insert a new line — always, even while the agent is replying (the unconditional form of Shift+Enter)",
    );
    // Fixed: the composer leaves ⌘Enter to the editor's own Mod-Enter
    // hardBreak; nothing registers a handler, so outside the composer the
    // chord keeps its native meaning.
    expect(def?.fixed).toBe(true);
    expect(keycaps("mod+enter")).toEqual(["⌘", "Enter"]);
    expect(matchCombo("mod+enter", ev("Enter", { meta: true }))).toBe(true);
    expect(matchCombo("mod+enter", ev("Enter", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+enter", ev("Enter"))).toBe(false);
    // The live description stays the registry's — only the two Enter rows
    // are phrased from the preference.
    expect(def && describeShortcut(def, "queue")).toBe(def?.description);
  });

  it("phrases Esc's layering: selection, then the pending ask, then the side panel, then the run", () => {
    expect(SHORTCUTS.find((s) => s.id === "close.esc")?.description).toBe(
      "Clear the selection, deny the pending permission ask, close the side panel — or stop the running turn",
    );
  });

  it("lists exactly one live Esc binding — the layered close.esc", () => {
    // The composer's Esc rows are fixed (documentation-only); the dispatcher
    // resolves Esc to `close.esc` alone, which is what lets one press deny
    // an ask, close a panel or stop a run without ever doing two.
    const live = SHORTCUTS.filter((s) => s.combo === "esc" && !s.fixed);
    expect(live.map((s) => s.id)).toEqual(["close.esc"]);
  });

  it("binds the TUI's approval verdict keys in their own Approvals group", () => {
    const byId = new Map(SHORTCUTS.map((s) => [s.id, s]));
    // cmd/mecatui/ui/keys.go: Allow = a/y (+ enter), AllowAlways = w,
    // Deny = d/n (+ esc). Enter is native on the focused button; Esc rides
    // close.esc.
    expect(byId.get("approval.allow")?.combo).toBe("y");
    expect(byId.get("approval.allow.alt")?.combo).toBe("a");
    expect(byId.get("approval.always")?.combo).toBe("w");
    expect(byId.get("approval.deny")?.combo).toBe("n");
    expect(byId.get("approval.deny.alt")?.combo).toBe("d");
    for (const id of [
      "approval.allow",
      "approval.allow.alt",
      "approval.always",
      "approval.deny",
      "approval.deny.alt",
    ]) {
      const def = byId.get(id);
      expect(def?.group).toBe("Approvals");
      // Dispatched (ChatView registers handlers while an ask waits), so NOT
      // documentation-only — and rebindable like the other letter keys.
      expect(def?.fixed).toBeUndefined();
      expect(def?.locked).toBeUndefined();
      // A bare letter: never fires while typing in a text field.
      expect(comboFiresWhileTyping(def?.combo ?? "")).toBe(false);
    }
    expect(SHORTCUT_GROUPS).toContain("Approvals");
    // The verdict-bar traversal row is component-owned documentation.
    expect(byId.get("approval.focus")?.combo).toBe("right");
    expect(byId.get("approval.focus")?.fixed).toBe(true);
    expect(byId.get("approval.focus")?.group).toBe("Approvals");
  });

  it("matches the verdict letters without modifiers only", () => {
    expect(matchCombo("y", ev("y"))).toBe(true);
    expect(matchCombo("y", ev("Y"))).toBe(true); // Caps Lock capital
    expect(matchCombo("y", ev("y", { meta: true }))).toBe(false);
    expect(matchCombo("y", ev("y", { ctrl: true }))).toBe(false);
    expect(matchCombo("y", ev("Y", { shift: true }))).toBe(false);
    expect(matchCombo("w", ev("w"))).toBe(true);
    expect(matchCombo("n", ev("n"))).toBe(true);
    expect(matchCombo("n", ev("n", { alt: true }))).toBe(false);
    expect(keycaps("y")).toEqual(["Y"]);
  });

  it("locks Esc: dispatched (a handler is registered) but never user-rebindable", () => {
    const def = SHORTCUTS.find((s) => s.id === "close.esc");
    expect(def?.locked).toBe(true);
    // Locked is NOT fixed — the dispatcher still fires it, so the keymap's
    // collision checks keep it in scope.
    expect(def?.fixed).toBeUndefined();
    for (const s of SHORTCUTS) {
      if (s.id !== "close.esc") expect(s.locked).toBeUndefined();
    }
  });
});

describe("keycaps", () => {
  it("renders modifiers and keys", () => {
    expect(keycaps("mod+k")).toEqual(["⌘", "K"]);
    expect(keycaps("mod+shift+n")).toEqual(["⌘", "⇧", "N"]);
    expect(keycaps("down")).toEqual(["↓"]);
    expect(keycaps("?")).toEqual(["?"]);
    expect(keycaps("shift+enter")).toEqual(["⇧", "Enter"]);
  });

  it("labels the paging keys as PgUp / PgDn", () => {
    expect(keycaps("pageup")).toEqual(["PgUp"]);
    expect(keycaps("shift+pageup")).toEqual(["⇧", "PgUp"]);
    expect(keycaps("shift+pagedown")).toEqual(["⇧", "PgDn"]);
  });

  it("labels the keys only a user-recorded combo can carry", () => {
    expect(keycaps("mod+space")).toEqual(["⌘", "Space"]);
    expect(keycaps("alt+f5")).toEqual(["⌥", "F5"]);
    expect(keycaps("shift+tab")).toEqual(["⇧", "Tab"]);
    expect(keycaps("mod+backspace")).toEqual(["⌘", "⌫"]);
    // matchCombo understands the same tokens (the space bar reports " ").
    expect(matchCombo("mod+space", ev(" ", { meta: true }))).toBe(true);
    expect(matchCombo("mod+space", ev(" "))).toBe(false);
    expect(matchCombo("alt+f5", ev("F5", { alt: true }))).toBe(true);
  });
});

describe("matchCombo", () => {
  it("matches modifier combos (⌘ or Ctrl)", () => {
    expect(matchCombo("mod+k", ev("k", { meta: true }))).toBe(true);
    expect(matchCombo("mod+k", ev("k", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+k", ev("k"))).toBe(false); // no modifier
  });

  it("matches plain keys and arrow aliases", () => {
    expect(matchCombo("j", ev("j"))).toBe(true);
    // A Caps Lock capital (no shiftKey) still matches its lower-case combo…
    expect(matchCombo("j", ev("J"))).toBe(true);
    // …but a HELD shift is a different chord (⇧J is not J, ⌘⇧K is not ⌘K).
    expect(matchCombo("j", ev("J", { shift: true }))).toBe(false);
    expect(matchCombo("mod+k", ev("K", { meta: true, shift: true }))).toBe(
      false,
    );
    expect(matchCombo("down", ev("ArrowDown"))).toBe(true);
    expect(matchCombo("up", ev("ArrowUp"))).toBe(true);
  });

  it("matches symbol keys without needing an explicit shift", () => {
    expect(matchCombo("?", ev("?", { shift: true }))).toBe(true);
    expect(matchCombo("/", ev("/"))).toBe(true);
  });

  it("rejects when a modifier is present but not wanted", () => {
    expect(matchCombo("j", ev("j", { meta: true }))).toBe(false);
    expect(matchCombo("down", ev("ArrowDown", { alt: true }))).toBe(false);
  });

  it("requires shift when the combo declares it", () => {
    expect(
      matchCombo("mod+shift+n", ev("n", { meta: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+n", ev("n", { meta: true }))).toBe(false);
  });

  it("matches the agents panel chord on either modifier, with shift", () => {
    // Shift+L reports an upper-case key in browsers; the match is case-blind.
    expect(
      matchCombo("mod+shift+l", ev("L", { meta: true, shift: true })),
    ).toBe(true);
    expect(
      matchCombo("mod+shift+l", ev("L", { ctrl: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+l", ev("l", { meta: true }))).toBe(false);
    expect(matchCombo("mod+shift+l", ev("l", { shift: true }))).toBe(false);
  });

  it("matches mod + punctuation combos", () => {
    expect(matchCombo("mod+,", ev(",", { meta: true }))).toBe(true);
    expect(matchCombo("mod+,", ev(",", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+,", ev(","))).toBe(false);
    expect(matchCombo("mod+/", ev("/", { meta: true }))).toBe(true);
    expect(matchCombo("mod+/", ev("/"))).toBe(false);
  });

  it("matches esc via its alias", () => {
    expect(matchCombo("esc", ev("Escape"))).toBe(true);
    expect(matchCombo("esc", ev("Escape", { meta: true }))).toBe(false);
  });

  it("matches the paging keys by their DOM key names", () => {
    expect(matchCombo("pageup", ev("PageUp"))).toBe(true);
    expect(matchCombo("pagedown", ev("PageDown"))).toBe(true);
    expect(matchCombo("shift+pageup", ev("PageUp", { shift: true }))).toBe(
      true,
    );
    expect(matchCombo("shift+pagedown", ev("PageDown", { shift: true }))).toBe(
      true,
    );
  });

  it("keeps the bare and shifted paging chords distinct", () => {
    // An unrequested shift on a NAMED key is a different chord (⇧PgUp must
    // reach `transcript.top`, never be swallowed by `pageup`)…
    expect(matchCombo("pageup", ev("PageUp", { shift: true }))).toBe(false);
    expect(matchCombo("pagedown", ev("PageDown", { shift: true }))).toBe(false);
    expect(matchCombo("shift+pageup", ev("PageUp"))).toBe(false);
    // …and so is a held shift on a letter, while SYMBOL keys keep their
    // implicit-shift leniency (`?` arrives with shiftKey on a US layout).
    expect(matchCombo("j", ev("J", { shift: true }))).toBe(false);
    expect(matchCombo("?", ev("?", { shift: true }))).toBe(true);
  });

  it("leaves the browser's Ctrl/⌘+PgUp/PgDn tab-switch chords alone", () => {
    expect(matchCombo("pageup", ev("PageUp", { ctrl: true }))).toBe(false);
    expect(matchCombo("pagedown", ev("PageDown", { meta: true }))).toBe(false);
    expect(
      matchCombo("shift+pageup", ev("PageUp", { ctrl: true, shift: true })),
    ).toBe(false);
  });
});

describe("comboFiresWhileTyping", () => {
  it("allows mod combos and bare esc, suppresses plain keys", () => {
    expect(comboFiresWhileTyping("mod+k")).toBe(true);
    expect(comboFiresWhileTyping("mod+shift+o")).toBe(true);
    // The agents panel must toggle from inside the composer too.
    expect(comboFiresWhileTyping("mod+shift+l")).toBe(true);
    expect(comboFiresWhileTyping("esc")).toBe(true);
    expect(comboFiresWhileTyping("j")).toBe(false);
    expect(comboFiresWhileTyping("?")).toBe(false);
    expect(comboFiresWhileTyping("shift+enter")).toBe(false);
  });

  it("lets the paging keys scroll the transcript from the composer", () => {
    expect(comboFiresWhileTyping("pageup")).toBe(true);
    expect(comboFiresWhileTyping("pagedown")).toBe(true);
    expect(comboFiresWhileTyping("shift+pageup")).toBe(true);
    expect(comboFiresWhileTyping("shift+pagedown")).toBe(true);
    // Home/End move the caret within a line while typing — never claimed.
    expect(comboFiresWhileTyping("home")).toBe(false);
    expect(comboFiresWhileTyping("end")).toBe(false);
  });
});

describe("describeShortcut", () => {
  const byId = (id: string) => {
    const def = SHORTCUTS.find((s) => s.id === id);
    if (!def) throw new Error(`missing shortcut ${id}`);
    return def;
  };

  it("phrases Enter live from the queue preference", () => {
    expect(describeShortcut(byId("composer.send"), "queue")).toBe(
      "Send — while the agent is replying: queue the message",
    );
    expect(describeShortcut(byId("composer.newline"), "queue")).toBe(
      "Insert a new line — while the agent is replying: steer the agent",
    );
  });

  it("inverts both rows when the preference is steer", () => {
    expect(describeShortcut(byId("composer.send"), "steer")).toBe(
      "Send — while the agent is replying: steer the agent",
    );
    expect(describeShortcut(byId("composer.newline"), "steer")).toBe(
      "Insert a new line — while the agent is replying: queue the message",
    );
  });

  // "Queue only" is the never-steer switch: neither row may promise a steer.
  it("promises no steer on either row under queue-only", () => {
    expect(describeShortcut(byId("composer.send"), "queue-only")).toBe(
      "Send — while the agent is replying: queue the message",
    );
    expect(describeShortcut(byId("composer.newline"), "queue-only")).toBe(
      "Insert a new line — while the agent is replying: queue the message (steering is off)",
    );
  });

  it("returns the registry description for every other shortcut", () => {
    for (const def of SHORTCUTS) {
      if (def.id === "composer.send" || def.id === "composer.newline") continue;
      expect(describeShortcut(def, "queue")).toBe(def.description);
      expect(describeShortcut(def, "steer")).toBe(def.description);
    }
  });
});
