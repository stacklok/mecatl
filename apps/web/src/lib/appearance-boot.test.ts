// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { describe, expect, it } from "vitest";

const html = readFileSync(new URL("../../index.html", import.meta.url), "utf8");
const boot = readFileSync(new URL("../../public/appearance-boot.js", import.meta.url), "utf8");

function runBoot(values: Record<string, string>, dark = false) {
  const calls: string[] = [];
  const classes = new Set<string>(["dark"]);
  const dataset: Record<string, string> = { palette: "stale" };
  const style = new Map<string, string>();
  const root = {
    classList: {
      toggle(name: string, active: boolean) {
        calls.push(`class:${name}:${active}`);
        if (active) classes.add(name);
        else classes.delete(name);
      },
    },
    dataset,
    removeAttribute(name: string) {
      calls.push(`remove:${name}`);
      if (name === "data-palette") delete dataset.palette;
    },
    style: {
      removeProperty(name: string) {
        style.delete(name);
      },
      setProperty(name: string, value: string) {
        style.set(name, value);
      },
    },
  };
  runInNewContext(boot, {
    document: { documentElement: root },
    window: {
      localStorage: {
        getItem(key: string) {
          calls.push(`read:${key}`);
          return values[key] ?? null;
        },
      },
      matchMedia: () => ({ matches: dark }),
    },
  });
  return { calls, classes, dataset, style };
}

describe("appearance boot", () => {
  it("applies stored appearance before the first stylesheet", () => {
    const script = '<script src="/appearance-boot.js"></script>';
    const bootOffset = html.indexOf(script);
    expect(bootOffset).toBeGreaterThan(html.indexOf("<head>"));
    expect(bootOffset).toBeLessThan(html.indexOf('<script type="module"'));
    const firstStyle = html.search(/<link[^>]+rel="stylesheet"|<style\b/);
    if (firstStyle >= 0) expect(bootOffset).toBeLessThan(firstStyle);

    const result = runBoot({
      "mecatl-studio-theme": "dark",
      "mecatl-studio.palette": "solar",
      "studio.profile.ui-scale": "1.2",
    });
    expect(result.classes.has("dark")).toBe(true);
    expect(result.dataset).toEqual({ palette: "solar", theme: "dark" });
    expect(result.style.get("--ui-scale")).toBe("1.2");
    expect(result.calls.filter((call) => call.startsWith("read:"))).toEqual([
      "read:mecatl-studio-theme",
      "read:mecatl-studio.palette",
      "read:studio.profile.ui-scale",
    ]);

    const system = runBoot(
      { "mecatl-studio-theme": "system", "mecatl-studio.palette": "mono" },
      true,
    );
    expect(system.classes.has("dark")).toBe(true);
    expect(system.dataset).toEqual({ palette: "mono", theme: "system" });
  });

  it("uses safe defaults for invalid choices and a missing media query", () => {
    const result = runBoot({
      "mecatl-studio-theme": "invalid",
      "mecatl-studio.palette": "external",
      "studio.profile.ui-scale": "nonsense",
    });
    expect(result.classes.has("dark")).toBe(false);
    expect(result.dataset).toEqual({ theme: "system" });
    expect(result.style.has("--ui-scale")).toBe(false);

    const classes = new Set<string>();
    const dataset: Record<string, string> = {};
    expect(() =>
      runInNewContext(boot, {
        document: {
          documentElement: {
            classList: { toggle: (name: string, enabled: boolean) => enabled && classes.add(name) },
            dataset,
            removeAttribute: () => {},
            style: { removeProperty: () => {}, setProperty: () => {} },
          },
        },
        window: {
          get localStorage() {
            throw new Error("denied");
          },
          matchMedia: () => {
            throw new Error("denied");
          },
        },
      }),
    ).not.toThrow();
    expect(classes.has("dark")).toBe(false);
    expect(dataset).toEqual({ theme: "system" });

    const withoutMedia = runInNewContext(boot, {
      document: {
        documentElement: {
          classList: { toggle: (_name: string, enabled: boolean) => expect(enabled).toBe(false) },
          dataset: {},
          removeAttribute: () => {},
          style: { removeProperty: () => {}, setProperty: () => {} },
        },
      },
      window: { localStorage: { getItem: () => null } },
    });
    expect(withoutMedia).toBeUndefined();
  });
});
