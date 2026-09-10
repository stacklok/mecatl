## 1. Prerequisites & install

### Install a release build

If you only need to *run* Mecatl, you do not need this toolchain. Homebrew
installs `mecatui` and `mecated` from the latest release:

```sh
brew install stacklok/tap/mecatl
```

Each `vX.Y.Z` tag also attaches signed `darwin`/`linux` x `amd64`/`arm64`
archives with checksums, cosign bundles, SBOMs, and build provenance to its
GitHub release. The public [install guide](https://mecatl.dev/docs/install)
covers archive verification and the Homebrew prefix. The rest of this section is
the **from-source** path, which is what a contributor and every `task` target
below need.

### Build from source

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | >= 1.26.6 (toolchain auto-resolves from `go.mod`) | building & running everything |
| [go-task](https://taskfile.dev) | v3 | the `task` build targets |
| [golangci-lint](https://golangci-lint.run/) | v2.x | `task lint` (config: `.golangci.yml`) |
| `goimports` | — | `task fmt` only |
| [buf](https://buf.build/docs/installation) | latest | `task generate` only — regenerating the proto |

Build the binaries into `bin/`:

```console
$ task build
go build -o bin/mecated ./cmd/mecated
go build -o bin/mecademo ./cmd/mecademo
go build -o bin/mecatequi ./cmd/mecatequi
go build -o bin/mecak8s ./cmd/mecak8s
go build -o bin/mecatui ./cmd/mecatui
```

This produces `bin/mecated` (the server), `bin/mecatui` (the terminal UI),
`bin/mecademo` (the offline demo), `bin/mecatequi` (the single-shot headless
CI/batch runner — see §15), and `bin/mecak8s` (the Kubernetes-native agent). To
install the operator-facing binaries into
`GOBIN` / `GOPATH/bin`:

```console
$ task install
```

`task install` installs `mecated` and `mecatui`; it intentionally skips
`mecademo`, which is only a demo binary. Both `task build` and `task install`
resolve a build-time source identity with
`git describe --tags --match 'v[0-9]*' --always --dirty`: most recent root release
tag, commit count, abbreviated SHA, and an optional `-dirty` suffix (for example,
`v0.0.22-28-g40a6b3fc6-dirty`). To stamp an explicit release or operator ID
instead, run `BUILD_ID=<value> task build` or `BUILD_ID=<value> task install`;
the supplied value, including `dev`, is retained verbatim. Direct unstamped Go or
ko builds use the runtime embedded-VCS fallback (`dev+<12-char-vcs-revision>[.dirty]`
or `dev`) and never invoke git at runtime.

The repo is a **Go workspace** (a committed `go.work`) spanning seven modules: the
root (`github.com/stacklok/mecatl`), the importable core
(`github.com/stacklok/mecatl/engine`), the opt-in caller-identity adapter
(`authn/oidc`), and the opt-in provider adapters (`provider/anthropic`,
`provider/openai`, `provider/openaichat`, `provider/ssefilter`). `task build`
builds all of them. A plain
`go build ./...` from the repo root does not cross the module boundary, so the
Taskfile runs `cd engine && go build ./...` for you; the workspace lets the root
module build against the in-tree engine via a `replace` directive.

Other handy targets (`task --list` for the full set):

| Task | What it does |
| --- | --- |
| `task build` | compile `bin/mecated`, `bin/mecademo`, `bin/mecatequi`, `bin/mecak8s`, `bin/mecatui` + build the engine module |
| `task install` | install `mecated` and `mecatui` into `GOBIN` / `GOPATH/bin` |
| `task test` | `go test -race ./...` (root) + the engine module + the `GOWORK=off` standalone hygiene proof |
| `task test:engine-standalone` | `cd engine && GOWORK=off go build ./... && go test ./...` — proves the engine's tiny closure is self-contained |
| `task test:cover` | tests + `coverage/coverage.{out,html}` (root + engine) |
| `task test:golden` | refresh the `mecatui` View/teatest golden files (`-update`), then re-run them |
| `task e2e` | the **LIVE** e2e suite against OpenRouter — real money + network, needs `OPENROUTER_API_KEY` (see §15) |
| `task fuzz` | bounded coverage-guided fuzzing of the security-critical parsers (`FUZZTIME=2m task fuzz`); not part of `task test` |
| `task lint` | `golangci-lint run` + `go vet` over the root module **and** the engine module (shared config) |
| `task vuln` | `govulncheck` (reachable-vuln scan) over the root module **and** the engine module; needs the network for the vuln DB, **not** part of `task test` |
| `task fmt` | `gofmt` + `goimports` |
| `task tidy` | tidy both `go.mod` files: root tidy → `go work sync` → engine `GOWORK=off go mod tidy` |
| `task generate` | `buf generate` (no-op unless `buf` + `contracts/proto` present) |
| `task ci` | tidy → fmt → lint → test → build |

You do **not** need a network or an API key for `task build`, `task test`, or
the offline demo.

The default `mecated` build is CGO-free and statically linkable (the ko image
builds it with `CGO_ENABLED=0`).

### Consuming `engine` as a library

The importable core is its own Go module,
`github.com/stacklok/mecatl/engine` ([ADR 0036](../adr/0036-engine-module.md)). An
external project depends on it directly, without dragging in mecatl's heavy
require cone (the OpenAI/Anthropic SDKs, gRPC, the TUI stack, client-go, …):

```console
$ go get github.com/stacklok/mecatl/engine@latest
```

```go
import (
    "github.com/stacklok/mecatl/engine/agent"
    "github.com/stacklok/mecatl/engine/session"
    "github.com/stacklok/mecatl/engine/adapter/mockllm" // a reference adapter, for tests
)
```

The engine module's entire dependency closure is `golang.org/x/sync`,
`github.com/bmatcuk/doublestar/v4`, and (test-only) `go.uber.org/goleak` — so a
consumer's build graph, SBOM, and vulnerability surface stay small. The
ports the loop consumes (`engine/port`: `LLMProvider`, `SessionStore`, …) are
interfaces you implement or wire to the in-tree reference adapters under
`engine/adapter/*`.

If your system-of-record is an **append-only event log** rather than a snapshot
store, the `engine/adapter/eventsource` reference fold (`eventsource.Fold`,
[ADR 0038](../adr/0038-event-sourced-rehydration.md)) reconstructs a `Session` by
replaying the durable `port.EventLog` stream — including the log-only
`EvUserPrompt` events the loop emits at every user-message record site. It is the
reference implementation of a `port.SessionStore.Load` for an event-sourced
consumer, so you can back the same loop with events instead of snapshots.

The module is released under submodule tags of the form `engine/vX.Y.Z` (Go's
convention for a module in a subdirectory), separate from the root `vX.Y.Z`
release tags, which drive the container images, the release archives, and the
Homebrew formula.

Working IN this repo, the committed `go.work` makes the root build against the
in-tree engine automatically. To reproduce a downstream consumer's isolated view
locally — no workspace, engine resolved against its own `go.mod`/`go.sum` alone —
run with the workspace off:

```console
$ cd engine && GOWORK=off go build ./... && GOWORK=off go test ./...
```

That is exactly what `task test:engine-standalone` (and the `engine-standalone`
CI job) run as the hygiene gate.

#### Supply-chain hygiene

CI runs `govulncheck` per module (the `vuln` job; `task vuln` locally) — a
**reachable**-vulnerability scan (call-graph analysis, not a bare require-list
scan) that reds the build on a finding. Both modules are scanned separately, so
the engine library's tiny closure is gated against its own dependencies
independently of the root's heavy cone — the hygiene travels with the engine
when a downstream consumer (e.g. a downstream consumer) pins it.

govulncheck has no native allowlist, so its JSON output is filtered through
`.github/scripts/govulncheck-gate.go` (a dependency-free `go run` filter, with
an offline self-test): it **fails on any new reachable vulnerability** while
accepting a small, documented allowlist. The **engine module is gated with NO
allowlist** — it is clean and must stay clean. The **root app allowlists exactly
two unfixable-upstream docker CVEs** (`GO-2026-4887`, `GO-2026-4883` in
`github.com/docker/docker`, transitive via `github.com/stacklok/toolhive`),
which are reachable **only** through the ToolHive adapter in the host module — never
the engine library — and have **no upstream fix** (Fixed: N/A). They are
accepted-risk (reviewed 2026-06-19) and the allowlist lives inline in the `vuln`
CI job; drop them the moment a fixed docker/toolhive lands. The gate still fails
on any *other* reachable vuln, and fails closed if govulncheck itself errors.

A `.github/dependabot.yml` keeps both `go.mod` files and the SHA-pinned GitHub
Actions current (weekly, minor+patch grouped to cut noise; the SHA-pin
`# vX.Y.Z` comments are preserved). See
[issue #118](https://github.com/stacklok/mecatl/issues/118).
