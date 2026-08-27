---
id: 02-provider-mode
title: "Truthful fixture provider modes"
blocked_by: [01-kind-base]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecak8s-kind-fixture
---

# Truthful fixture provider modes

On the new base fixture, retain offline mock mode by default. Make the
OpenRouter opt-in create a fixture-owned Secret through stdin, project one
environment variable through the Helm chart, disable mock mode, and remove the
fixture-owned provider Secret when switching back to mock mode. No setup or
default test may spend provider tokens.

## Acceptance criteria

- AC2.1: Without `OPENROUTER_API_KEY`, `mecak8s:kind-setup` renders the canned
  mock provider and makes no provider network request.
  - verify: `TestMecak8sKindFixture_Scenario2_MockDefault`
- AC2.2: With `OPENROUTER_API_KEY`, the rendered deployment omits `--mock` and
  includes exactly one `OPENROUTER_API_KEY` environment projection from the
  fixture-owned Secret.
  - verify: `TestMecak8sHelmChart_KindFixtureRealProviderDisablesMock`
- AC2.3: The fixture passes an OpenRouter credential to `kubectl` via standard
  input and `--from-file`; no fixture command, rendered manifest, pod argument,
  or diagnostic path prints the value or uses
  `--from-literal=OPENROUTER_API_KEY`.
  - verify: `TestInvariant_credential_not_process_argument`
- AC2.4: Switching the fixture from real-provider mode back to mock mode stops
  the Secret projection and deletes the fixture-owned provider Secret; a stale
  credential is never mounted by the mock deployment.
  - verify: `TestMecak8sKindFixture_Scenario2_ResetToMock`
- AC2.5: A real-provider smoke call is neither a setup action nor a default-test
  side effect; fixture instructions name it as a separate, explicit billable
  operator action.
  - verify: `TestMecak8sKindFixture_Scenario2_LiveSmokeIsExplicit`
