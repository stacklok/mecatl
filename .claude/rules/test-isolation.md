---
matlatl: orphan-intentional
paths:
  - "**/*_test.go"
---
# Test isolation

Keep tests offline and isolated from operator state. Prefer injected reference stores and adapters (`mockllm`, `memfs`, `memstore`) plus controlled clocks unless the test requires a real adapter. Real composition, filesystem, and restart tests must use explicit test-owned stores and directories; preserve explicit directories when state intentionally spans builds.

For ordinary composition tests use `buildIsolated`/`isolateConfig`, or the command package's equivalent. `NoUserModel` is not isolation: learned-skill storage resolves `UserModelDir` independently, so inject a temporary `UserModelDir` even when learning or user-model features are disabled.

HOME/XDG/default-discovery tests must be nonparallel and use a controlled synthetic environment. Propagate the same isolated paths and environment into subprocesses; direct `go test` must be isolated without relying on Taskfile overrides. Plant poisoned ambient state as a negative control and retain positive controls for explicitly injected state. Do not disable the behavior under test to gain speed or isolation.
