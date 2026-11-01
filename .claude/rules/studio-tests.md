---
matlatl: orphan-intentional
paths:
  - "apps/**/*.test.ts"
  - "apps/**/*.test.tsx"
  - "apps/web/src/test-*.ts"
  - "apps/web/e2e/**"
  - "apps/server/test/**"
---
# Studio tests

Studio tests are offline. Vitest runs both apps with `isolate: false`, so the files in a worker share one module cache, Web Storage, and the happy-dom `window`. Web tests run in Node unless the file starts with `// @vitest-environment happy-dom`. The NO-ISOLATE RULES in `apps/web/src/test-setup.ts` give the fixes, and Biome fails on the first two hazards:

- Render the real module inside real providers, with a fresh `QueryClient` and memory router per test, and fake the network with `apiClient.setConfig({ baseUrl: window.location.origin, fetch })`, where `apiClient` is `client` from `@mecatl-studio/contracts/client`; `vi.mock()` and `vi.doMock()` are banned.
- Spy on storage with `spyOnLocalStorage()` from `apps/web/src/test-storage.ts`; `vi.spyOn` on Web Storage is banned.
- Replace globals with `vi.stubGlobal()`, and give module-level state a reset that the shared hook in `test-setup.ts` calls.

Browser journeys in `apps/web/e2e/` import `test` and `expect` from `./fixtures`. Its automatic `offlineBff` fixture answers `/api/` calls only from the handlers a test registers, and fails the test on an unregistered BFF call or on network access outside its fixtures. Playwright runs each journey in the `chromium-desktop` (1280x800) and `chromium-mobile` (390x844) projects.

The approval journey and `apps/server/test/integration/` drive a real `mecated --mock` from `bin/mecated`; [the Studio agent contract](../../apps/AGENTS.md#commands) lists the commands.

Before changing behavior, pin the current behavior in its own commit, with `*.pinning.test.tsx` tests that pass on the base commit and on the branch.
