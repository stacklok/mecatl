# AGENTS.md: Studio workspace

`apps/` holds Mecatl Studio, a self-contained pnpm workspace outside the Go build:
`contracts/` (Zod schemas and the generated client), `server/` (the Hono backend for
frontend, or BFF), and `web/` (the Vite and React app). The [README](README.md)
covers local runtime modes. The public [Studio guide](../user-docs/operating/studio.md)
owns deployment, configuration, and the security model.

## Commands

Run these from the repository root:

```sh
task studio:check            # local gate: Biome, typecheck, Vitest, contracts drift
task studio:browser          # offline Playwright journeys, desktop and phone
task studio:test:integration # BFF over the real SDK against a spawned mecated --mock
task studio:generate         # regenerate the contracts artifacts
task studio:format           # apply Biome formatting and import order
```

- `task studio:check` must pass for any change under `apps/`. `task test` and
  `task lint` cover only the Go modules. Biome formats and lints all three packages
  from `biome.json`, so run `task studio:format` before committing. CI also runs
  `task studio:audit`, which fails on a production dependency with an advisory.
- The approval journey in `task studio:browser` spawns `mecated --mock`, so run
  `task build` first. Install Chromium once with `task studio:browser:install`.
- To run one journey, use `pnpm run test:browser <SPEC> --project=chromium-mobile`
  from `apps/`. Keep the `=`, because `--project` accepts several names. Leave out
  `--`, because Playwright ignores the arguments after it and runs every journey.
- `task studio:test:integration` runs the full `task build` first. To rerun the
  suite against an existing `bin/mecated`, run
  `cd apps && STUDIO_REQUIRE_MECATED=1 STUDIO_MECATED_BIN=$PWD/../bin/mecated pnpm --filter @mecatl-studio/server run test:integration`.
  Without `STUDIO_REQUIRE_MECATED=1`, a missing binary skips the suite.

## Generated files

Never edit these by hand; Biome skips them.

- `contracts/openapi.json` and `contracts/src/generated/` (the Hey API client and
  its TanStack Query helpers) come from the Hono routes. Regenerate them with
  `task studio:generate` (`pnpm run generate` in `apps/`) and commit them with the
  route or schema change; `task studio:generated-check` fails CI when they drift.
- `web/src/routeTree.gen.ts` comes from the files in `web/src/routes/`. The
  TanStack Router Vite plugin rewrites it during `dev` and `build`, and the web
  `typecheck` script runs `tsr generate`. Commit it with the route change.

## Boundaries

- The BFF is the single source of truth for the data the UI shows. `web/` requests
  data only from the BFF's `/api/v1`, through the generated client, and only labels
  or formats what comes back. It never re-derives state or eligibility; when the UI
  needs a value the contract lacks, add it to the schema and the BFF route.
- The design prototype, branch `stack/08-tooling` of `stacklok/mecatl-prototypes`,
  is the source of truth for components, layout, interaction, and copy. #2210 lists
  what Studio keeps, including the BFF, contracts, security, and contrast tokens.
- `server/` builds the checked-out `sdk/typescript` through a frozen `file:`
  dependency and imports it only as `@stacklok-oss/mecatl-sdk`.
  `server/src/sdk-source.test.ts` guards the dependency, and Biome rejects relative
  imports of `sdk/`. `web/` imports neither the SDK nor its protocol types.
- `MECATL_*` variables describe the target (`server/src/mecatl/runtime.ts`), and
  `STUDIO_*` variables configure Studio itself (`server/src/config.ts`). The Studio
  guide's [configuration reference](../user-docs/operating/studio.md#configuration-reference)
  owns the deployment variables.

## Path-scoped rules

These load with the files they cover:

- [`studio-bff.md`](../.claude/rules/studio-bff.md): `server/` and `contracts/`
  routes, problem details, and security invariants.
- [`studio-web.md`](../.claude/rules/studio-web.md): `web/src/` routes, features,
  data access, UI primitives, and the 500px breakpoint.
- [`studio-tests.md`](../.claude/rules/studio-tests.md): Vitest without isolation,
  the offline Playwright fixtures, and pinning tests.

## UI pull requests

A PR that changes what Studio renders shows screenshots in Before, After, and
Prototype columns at desktop (1280x800) and phone (390x844) sizes, in light and
dark, with the changed regions highlighted.

Capture Before and After with a throwaway `<NAME>.spec.ts` in `web/e2e/` that you
don't commit. Import `test` from `./fixtures`, answer each JSON call with
`offlineBff.json()` and each event stream with `offlineBff.on()` returning
`text/event-stream`, and fix the clock with
`page.clock.install({ time: new Date("<ISO_TIME>") })`. Save each shot with
`page.screenshot({ path: testInfo.outputPath("<NAME>.png") })`. The
`chromium-desktop` and `chromium-mobile` projects give both sizes, and
`page.emulateMedia({ colorScheme: "dark" })` switches the theme. Run the spec on
the base commit for Before and on the branch for After, copying the shots out of
`.scratch/studio-browser-playwright/` between runs, because Playwright empties it
at the start of each run. Take the Prototype column from the prototype branch
above. Recent PRs host the images on the `pr-assets` branch and link them as
`raw.githubusercontent.com` URLs.
