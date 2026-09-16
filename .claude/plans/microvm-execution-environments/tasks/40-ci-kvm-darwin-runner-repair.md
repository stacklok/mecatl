---
id: 40-ci-kvm-darwin-runner-repair
title: Fix hosted KVM access, Darwin socket length, and manual HVF gating
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/40-ci-kvm-darwin-runner-repair"
worktree: ".scratch/task-microvm-40"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

PR #580 latest failures:

- hosted Linux amd64 setup completes but microvmd still receives `permission denied` opening `/dev/kvm`; verify effective identity/ACL across the actual test process and use the standard hosted-runner KVM setup that survives subsequent steps.
- `TestDoctorProcessStartIdentityDistinguishesHealthyAndReusedPID` still uses an overlong Darwin UDS path; route every microvmd test socket through one short private cross-platform helper.
- controlled macOS HVF remains queued indefinitely with no self-hosted runner; make it explicitly manual/default-off like controlled arm64 KVM while retaining hosted Darwin compile/static.

Verification: contract tests prove Linux setup works in a later process, all test sockets respect Darwin limit, PR events skip unavailable controlled live cells, local KVM E2E and all standard/action gates pass.
