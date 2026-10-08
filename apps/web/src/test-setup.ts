// SPDX-License-Identifier: Apache-2.0

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, onTestFinished, vi } from "vitest";
import { clearUserScopedStorage } from "./lib/account-storage";
import { resetApiClientState } from "./lib/api-client";

/**
 * NO-ISOLATE RULES. Both apps run vitest with `isolate: false`, so every test
 * file in a worker shares one module cache and, for happy-dom, one `window`
 * and `document`. Biome (apps/lint/*.grit) fails the build on the first two
 * patterns below; a biome error pointing here means one of these applies.
 *
 *   BAD  vi.mock("some-module", ...)
 *        The module cache is shared, so the mock is bypassed by a module an
 *        earlier file already loaded, or leaks into the next file.
 *   GOOD render the real module inside real providers (a fresh QueryClient
 *        and a memory router per test) and fake the network instead:
 *        `apiClient.setConfig({ baseUrl: window.location.origin, fetch })`
 *        with `fetch = vi.fn<typeof globalThis.fetch>(...)`. See
 *        features/auth/auth-gate-account.test.tsx. To force a failure, make the
 *        faked request fail; do not stub the component.
 *
 *   BAD  vi.spyOn(Storage.prototype, "setItem") or vi.spyOn(window.localStorage, ...)
 *        happy-dom memoizes `localStorage` methods, so a prototype spy installed
 *        after the first call is silently ignored; and neither
 *        `vi.restoreAllMocks()` nor `restoreMocks` restores a spy on the
 *        `localStorage` instance, so it breaks every later file in the worker.
 *   GOOD spyOnLocalStorage("setItem") from ./test-storage (restores itself).
 *
 *   BAD  assigning to `window`/`globalThis`, or patching a prototype
 *        (HTMLElement.prototype.scrollIntoView = ...) inside a test
 *   GOOD vi.stubGlobal(...); the `unstubGlobals` flag undoes it per test.
 *
 *   BAD  a module-level singleton that tests mutate (a QueryClient, a store)
 *   GOOD build one per test, or give the module a reset like
 *        `clearUserScopedStorage`, and call it from the hook below.
 *
 * TERM: test-global state. Anything a test file can mutate that outlives the
 * file under `isolate: false`: Web Storage, module-level singletons, and the
 * happy-dom `window`/`document` (window properties and <body> attributes
 * survive into the next file).
 * Avoid: "leak" for the symptom, "pollution" for the state itself.
 *
 * DECISION: reset test-global state centrally, not in each file's own hooks.
 * Spies, stubbed globals and stubbed env are reset by `restoreMocks`,
 * `unstubGlobals` and `unstubEnvs` in vite.config.ts; this hook covers what
 * those cannot reach. Rejected: per-file cleanup conventions (they fail
 * silently, and only when worker scheduling is unlucky).
 *
 * DECISION: no blanket DOM wipe (`document.body.innerHTML = ""`, window
 * properties). Add a reset here only for state a serial `--no-isolate` run
 * proves is shared; the unproven ones are not worth the lines.
 *
 * SPEC: after any test, the text selection is cleared, fake timers and the fake
 * system clock are off, React Testing Library trees are unmounted, Web Storage
 * is empty, `account-storage`'s and `api-client`'s module-level state is reset,
 * and the shared generated client has its original `baseUrl` and `fetch`.
 * Testing Library registers its own auto-cleanup once per module load, which
 * under `isolate: false` means only the first file in a worker, so cleanup is
 * explicit here.
 */

// Captured at load, before any test can `vi.stubGlobal("window", ...)`: the hook
// below must clear the real stores, not whatever a test swapped in.
const realLocal = typeof window === "undefined" ? undefined : window.localStorage;
const realSession = typeof window === "undefined" ? undefined : window.sessionStorage;
const realWindow = typeof window === "undefined" ? undefined : window;

const initialApiConfig = apiClient.getConfig();

// SPEC: the production route tree is evaluated before any test builds a router.
// Observed with TanStack Router 1.17x: once any file has called `createRouter`,
// a route module first evaluated afterwards shares its `options` with that
// router's root, so the real root renders the earlier file's component. Root
// cause not established; evaluating the tree here, first, avoids it. Without
// this, error-routes.test.tsx fails whenever a router-building file runs first.
if (typeof document !== "undefined") await import("./routeTree.gen");

afterEach(() => {
  vi.useRealTimers();
  cleanup();
  resetApiClientState();
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
  // The text selection lives on the shared document; addRange() is a no-op while one exists.
  realWindow?.getSelection()?.removeAllRanges();
});

// DECISION: storage is reset from an `onTestFinished` registered here, not from
// `afterEach`. vitest runs `onTestFinished` callbacks after `afterEach` hooks,
// and last-registered first, so this one (registered before any test body
// runs) fires after a test's own restores, e.g. `spyOnLocalStorage`. Reset from
// `afterEach` while such a spy is active and `clearUserScopedStorage` fails,
// leaving `account-storage` quarantined for the next file's first test.
beforeEach(() => {
  onTestFinished(() => {
    clearUserScopedStorage(realLocal, realSession);
    realLocal?.clear();
    realSession?.clear();
  });
});
