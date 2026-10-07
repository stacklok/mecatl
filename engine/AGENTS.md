# AGENTS.md: engine module

`engine/` is the separately published `github.com/stacklok/mecatl/engine` module.

## Commands

- Focused test: `cd engine && go test ./<pkg>/ -run <Test>`.
- `task test:engine-standalone` builds and tests with `GOWORK=off`. A new import must
  resolve from `engine/go.mod` alone, never from the root module.
- The API snapshot tool lives in the root module (`internal/apicheck`), so run
  `task api:update` from the repo root after changing an exported identifier in a
  package listed in `arch.CorePackages`. Commit the `engine/api/*.txt` changes and add
  an `engine/CHANGELOG.md` entry classified per [COMPATIBILITY.md](COMPATIBILITY.md).

## Layering is enforced twice

A new import between engine packages must be allowed in both places, or lint and tests
fail independently:

- the package's strict depguard allowlist in the root `.golangci.yml`;
- `coreImportRules` in `arch/layering_test.go` (core packages only).

A new core package also goes in `arch.CorePackages`, which feeds the layering tests and
the API snapshot gate. Production code in `engine/adapter/*` never imports `agent`.

## Invariants

- `FileSystem`, `Workspace`, and `Environment` belong to `tool`, not `port`: `port`
  imports `tool`, so the reverse would cycle.
- `governance` stays session-free and imports only the standard library; `session` may
  carry governance values.
- Mutate `Session` only through its aggregate methods, and keep tool-call/result
  pairing valid.
- `port.LLMRequest` stays provider-neutral and provider replay stateless. Re-derive
  provider- or model-dependent dependencies through factories, not clone-and-swap.
