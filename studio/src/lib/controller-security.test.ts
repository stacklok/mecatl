import { describe, expect, it } from "vitest";
import { requestIsAllowed, validSkillName } from "./controller-security.mjs";

/**
 * The shared skill-name gate — the ONE grammar (mirroring the daemon's
 * skillfs.ValidSkillName) that both the controller's filesystem endpoints and
 * the browser client validate through. Everything the filesystem side relies
 * on (no separators, no dots, no traversal) must hold here, because a valid
 * name is joined directly onto the pinned skills directory.
 */
describe("validSkillName", () => {
  it("accepts the daemon's activation-name grammar", () => {
    for (const name of [
      "a",
      "pr-feedback",
      "skill_2",
      "0day-notes",
      "x".repeat(64),
    ]) {
      expect(validSkillName(name), name).toBe(true);
    }
  });

  it("rejects path traversal and separators", () => {
    for (const name of [
      "..",
      ".",
      "../evil",
      "a/../b",
      "a/b",
      "a\\b",
      ".disabled",
      ".hidden",
      "name.md",
    ]) {
      expect(validSkillName(name), name).toBe(false);
    }
  });

  it("rejects whitespace, case, emptiness, and oversized names", () => {
    for (const name of [
      "",
      " ",
      "two words",
      "Upper",
      "tab\tname",
      "new\nline",
      "-leading-dash",
      "_leading-underscore",
      "x".repeat(65),
    ]) {
      expect(validSkillName(name), JSON.stringify(name)).toBe(false);
    }
  });
});

/**
 * The controller's request gate. Reads of the status-shaped documents
 * (`/status`, `/model-router`, `/daemon-defaults`) are header-free so the
 * runtime poll can read them; EVERY write — the daemon-defaults PUT
 * included — still needs the server-set studio header on top of the
 * loopback Host and allowlisted Origin (studio/CLAUDE.md rule 4).
 */
describe("requestIsAllowed", () => {
  const options = {
    allowedOrigins: new Set(["http://localhost:3000"]),
    mcpProxyPrefix: "/mcp-proxy/",
  };
  const request = (
    method: string,
    headers: Record<string, string> = {},
  ): { method: string; headers: Record<string, string> } => ({
    method,
    headers: { host: "127.0.0.1:8788", ...headers },
  });
  const url = (pathname: string) => new URL(`http://127.0.0.1:8788${pathname}`);

  it("lets the read-only daemon-defaults GET through without the studio header", () => {
    expect(
      requestIsAllowed(request("GET"), url("/daemon-defaults"), options),
    ).toBe(true);
    expect(requestIsAllowed(request("GET"), url("/status"), options)).toBe(
      true,
    );
    expect(
      requestIsAllowed(request("GET"), url("/model-router"), options),
    ).toBe(true);
  });

  it("refuses the daemon-defaults PUT (and every other write) without the header", () => {
    expect(
      requestIsAllowed(request("PUT"), url("/daemon-defaults"), options),
    ).toBe(false);
    expect(
      requestIsAllowed(request("POST"), url("/providers/active"), options),
    ).toBe(false);
    expect(
      requestIsAllowed(
        request("PUT", { "x-mecatl-studio-request": "1" }),
        url("/daemon-defaults"),
        options,
      ),
    ).toBe(true);
  });

  it("gates /daemon-options in BOTH directions: it names directories on this machine and the PUT restarts the daemon", () => {
    // Deliberately NOT in the header-free read list (unlike /status): the
    // GET reports the skills/memory/user-model directories, and the proxy
    // always adds the header for the UI's own reads.
    expect(
      requestIsAllowed(request("GET"), url("/daemon-options"), options),
    ).toBe(false);
    expect(
      requestIsAllowed(request("PUT"), url("/daemon-options"), options),
    ).toBe(false);
    expect(
      requestIsAllowed(
        request("GET", { "x-mecatl-studio-request": "1" }),
        url("/daemon-options"),
        options,
      ),
    ).toBe(true);
    expect(
      requestIsAllowed(
        request("PUT", { "x-mecatl-studio-request": "1" }),
        url("/daemon-options"),
        options,
      ),
    ).toBe(true);
  });

  it("still refuses a non-loopback Host or a foreign Origin on the read", () => {
    expect(
      requestIsAllowed(
        request("GET", { host: "evil.example" }),
        url("/daemon-defaults"),
        options,
      ),
    ).toBe(false);
    expect(
      requestIsAllowed(
        request("GET", { origin: "https://evil.example" }),
        url("/daemon-defaults"),
        options,
      ),
    ).toBe(false);
  });
});
