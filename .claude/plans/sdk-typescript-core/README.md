# sdk-typescript-core — plan index

M1 of `@stacklok/mecatl-sdk`: scaffold, codegen, transports, Client/Session/Run,
events, permissions, multimodal helpers, offline e2e.

Delivery is a **linear `gh stack`** rooted at `sdk/10-architecture-adr`
(PR #908 / ADR 0279). Parallelism in scenarios 4–8 is serialised so each
layer is one stacked PR.

Plan: `docs/acceptance/sdk-typescript-core.md`. Parent issue: #821.

## Tasks

- [01-scaffold](tasks/01-scaffold.md) — `sdk/typescript/` tree, toolchain, CI, actrace resolver. AC1.1–1.6. Issue #909. Branch `sdk/11-scaffold`.
- [02-codegen](tasks/02-codegen.md) — second buf template, freshness gate. AC2.1–2.4. Issue #910. Branch `sdk/12-codegen`. Blocked by 01.
- [03-transports](tasks/03-transports.md) — Connect-ES + HTTP/SSE, errors, floor. AC3.1–3.9. Issue #911. Branch `sdk/13-transports`. Blocked by 02.
- [04-client-session](tasks/04-client-session.md) — Client/Session/status. AC4.1–4.5. Issue #912. Branch `sdk/14-client-session`. Blocked by 03.
- [05-run](tasks/05-run.md) — run choreography, stale controls, strict steer. AC5.1–5.7. Issue #913. Branch `sdk/15-run`. Blocked by 04.
- [06-events](tasks/06-events.md) — event unions + Go↔TS kind parity. AC6.1–6.4. Issue #914. Branch `sdk/16-events`. Blocked by 05 (serialised; only needs 03).
- [07-permissions](tasks/07-permissions.md) — asks + `run.resolveAsk`. AC7.1–7.5. Issue #915. Branch `sdk/17-permissions`. Blocked by 06.
- [08-media](tasks/08-media.md) — multimodal helpers. AC8.1–8.3. Issue #916. Branch `sdk/18-media`. Blocked by 07.
- [09-e2e](tasks/09-e2e.md) — `--mock-script` + offline e2e + CI job completeness. AC9.1–9.5. Issue #917. Branch `sdk/19-e2e`. Blocked by 08.

## Dependency graph (serial for gh stack)

```
01-scaffold → 02-codegen → 03-transports → 04-client-session
  → 05-run → 06-events → 07-permissions → 08-media → 09-e2e
```
