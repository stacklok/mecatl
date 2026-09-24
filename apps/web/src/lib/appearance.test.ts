// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from "vitest";
import { BUILT_IN_PALETTES } from "./palettes";
import { createAppearanceStore } from "./theme";

function browser(values: Record<string, string> = {}) {
  const entries = new Map(Object.entries(values));
  const classes = new Set<string>();
  const dataset: Record<string, string> = {};
  const styles = new Map<string, string>();
  const frames = new Map<number, FrameRequestCallback>();
  const listeners = new Map<string, Set<(event: Event) => void>>();
  const mediaListeners = new Set<() => void>();
  let frameId = 0;
  let dark = false;
  const root = {
    classList: {
      add(name: string) {
        classes.add(name);
      },
      contains(name: string) {
        return classes.has(name);
      },
      remove(name: string) {
        classes.delete(name);
      },
      toggle(name: string, enabled: boolean) {
        if (enabled) classes.add(name);
        else classes.delete(name);
      },
    },
    dataset,
    removeAttribute(name: string) {
      if (name === "data-palette") delete dataset.palette;
    },
    style: {
      removeProperty(name: string) {
        styles.delete(name);
      },
      setProperty(name: string, value: string) {
        styles.set(name, value);
      },
    },
  };
  const storage = {
    getItem: vi.fn((key: string) => entries.get(key) ?? null),
    removeItem: vi.fn((key: string) => entries.delete(key)),
    setItem: vi.fn((key: string, value: string) => entries.set(key, value)),
  };
  const media = {
    get matches() {
      return dark;
    },
    addEventListener(_name: string, listener: () => void) {
      mediaListeners.add(listener);
    },
    removeEventListener(_name: string, listener: () => void) {
      mediaListeners.delete(listener);
    },
  };
  vi.stubGlobal("document", { documentElement: root });
  vi.stubGlobal("window", {
    localStorage: storage,
    matchMedia: () => media,
    addEventListener(name: string, listener: (event: Event) => void) {
      const set = listeners.get(name) ?? new Set();
      set.add(listener);
      listeners.set(name, set);
    },
    removeEventListener(name: string, listener: (event: Event) => void) {
      listeners.get(name)?.delete(listener);
    },
    requestAnimationFrame(callback: FrameRequestCallback) {
      const id = ++frameId;
      frames.set(id, callback);
      return id;
    },
    cancelAnimationFrame(id: number) {
      frames.delete(id);
    },
  });
  return {
    classes,
    dataset,
    entries,
    media,
    root,
    storage,
    emitStorage(key: string | null) {
      for (const listener of listeners.get("storage") ?? []) listener({ key } as StorageEvent);
    },
    setSystemDark(next: boolean) {
      dark = next;
      for (const listener of mediaListeners) listener();
    },
    runFrame() {
      const pending = [...frames.values()];
      frames.clear();
      for (const callback of pending) callback(0);
    },
  };
}

describe("appearance state", () => {
  it("persists theme and palette across reload", () => {
    const env = browser();
    const first = createAppearanceStore();
    first.initialize();
    first.setTheme("dark");
    first.setPalette("aztec");
    expect(env.entries.get("mecatl-studio-theme")).toBe("dark");
    expect(env.entries.get("mecatl-studio.palette")).toBe("aztec");
    expect(env.classes.has("dark")).toBe(true);
    expect(env.dataset.palette).toBe("aztec");

    const reloaded = createAppearanceStore();
    expect(reloaded.getSnapshot()).toMatchObject({
      effectiveTheme: "dark",
      palette: "aztec",
      theme: "dark",
    });
    reloaded.setTheme("system");
    reloaded.setPalette("default");
    expect(env.entries.has("mecatl-studio-theme")).toBe(false);
    expect(env.entries.has("mecatl-studio.palette")).toBe(false);
    expect(env.dataset.palette).toBeUndefined();
  });

  it("keeps an in-memory choice when storage is unavailable", () => {
    const env = browser();
    env.storage.getItem.mockImplementation(() => {
      throw new Error("denied");
    });
    env.storage.setItem.mockImplementation(() => {
      throw new Error("denied");
    });
    env.storage.removeItem.mockImplementation(() => {
      throw new Error("denied");
    });
    const store = createAppearanceStore();
    store.initialize();
    store.setTheme("dark");
    store.setPalette("solar");
    env.emitStorage("unrelated-key");
    env.setSystemDark(true);
    expect(store.getSnapshot()).toMatchObject({ theme: "dark", palette: "solar" });
    expect(env.classes.has("dark")).toBe(true);
    expect(env.dataset.palette).toBe("solar");

    const reloaded = createAppearanceStore();
    expect(reloaded.getSnapshot()).toMatchObject({ theme: "system", palette: "default" });
    expect(reloaded.getSnapshot().effectiveTheme).toBe("dark");
  });

  it("keeps a new choice when stored values are readable but writes fail", () => {
    const env = browser({ "mecatl-studio-theme": "light", "mecatl-studio.palette": "aztec" });
    env.storage.setItem.mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    const store = createAppearanceStore();
    store.initialize();
    expect(store.getSnapshot()).toMatchObject({ theme: "light", palette: "aztec" });

    store.setTheme("dark");
    store.setPalette("solar");
    env.emitStorage("unrelated-key");
    expect(store.getSnapshot()).toMatchObject({ theme: "dark", palette: "solar" });
    expect(env.entries.get("mecatl-studio-theme")).toBe("light");
    expect(env.entries.get("mecatl-studio.palette")).toBe("aztec");

    const reloaded = createAppearanceStore();
    expect(reloaded.getSnapshot()).toMatchObject({ theme: "light", palette: "aztec" });
  });

  it("suppresses color transitions while appearance changes", () => {
    const env = browser();
    const store = createAppearanceStore();
    store.initialize();
    store.setTheme("dark");
    expect(env.classes.has("appearance-changing")).toBe(true);
    expect(env.classes.has("dark")).toBe(true);
    env.runFrame();
    expect(env.classes.has("appearance-changing")).toBe(true);
    store.setPalette("mono");
    store.setTheme("system");
    env.setSystemDark(true);
    expect(env.classes.has("appearance-changing")).toBe(true);
    env.runFrame();
    env.runFrame();
    expect(env.classes.has("appearance-changing")).toBe(false);
  });

  it("filters storage notifications and follows system mode without changing palette", () => {
    const env = browser({ "mecatl-studio.palette": "mono" });
    const store = createAppearanceStore();
    let notifications = 0;
    const stop = store.subscribe(() => notifications++);
    let secondControlNotifications = 0;
    const stopSecond = store.subscribe(() => secondControlNotifications++);
    store.initialize();
    expect(store.getSnapshot()).toMatchObject({ theme: "system", palette: "mono" });
    env.setSystemDark(true);
    expect(store.getSnapshot()).toMatchObject({ effectiveTheme: "dark", palette: "mono" });
    env.entries.set("mecatl-studio-theme", "light");
    env.emitStorage("unrelated-key");
    expect(store.getSnapshot().theme).toBe("system");
    env.emitStorage("mecatl-studio-theme");
    expect(store.getSnapshot()).toMatchObject({ effectiveTheme: "light", theme: "light" });
    env.entries.clear();
    env.emitStorage(null);
    expect(store.getSnapshot()).toMatchObject({ theme: "system", palette: "default" });
    expect(notifications).toBeGreaterThan(2);
    expect(secondControlNotifications).toBe(notifications);
    stop();
    stopSecond();
  });

  it("validates stored values and tolerates a missing or failing media query", () => {
    const env = browser({ "mecatl-studio-theme": "blue", "mecatl-studio.palette": "unknown" });
    vi.stubGlobal("window", {
      ...window,
      matchMedia: () => {
        throw new Error("unavailable");
      },
    });
    const store = createAppearanceStore();
    expect(store.getSnapshot()).toMatchObject({
      effectiveTheme: "light",
      palette: "default",
      theme: "system",
    });
    expect(env.storage.getItem).toHaveBeenCalledWith("mecatl-studio-theme");
    expect(env.storage.getItem).toHaveBeenCalledWith("mecatl-studio.palette");
  });

  it("publishes the fixed palette catalogue", () => {
    expect(BUILT_IN_PALETTES).toEqual([
      {
        id: "default",
        label: "Default",
        description: "Stacklok green.",
        swatch: "hsl(161 94% 21%)",
      },
      {
        id: "aztec",
        label: "Aztec",
        description: "Jade, turquoise and gold on obsidian.",
        swatch: "#0f7f6c",
      },
      {
        id: "mono",
        label: "Mono",
        description: "Neutral greys with a blue accent.",
        swatch: "#2563eb",
      },
      { id: "solar", label: "Solar", description: "Warm and light-leaning.", swatch: "#9a7300" },
    ]);
  });
});
