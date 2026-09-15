---
id: 43-ci-multi-identity-kvm-peer-proof
title: Separate authorized VM owner identities from wrong-peer probes
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/43-ci-multi-identity-kvm-peer-proof"
worktree: ".scratch/task-microvm-43"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

PR #580 KVM live failures now occur under UID/EUID 1001 during child/Parallel/Team launches, while `/dev/kvm` ACL authorizes only runner UID 1000. Trace why production E2E changes the daemon/runtime identity. Child environments owned by the same daemon must retain its KVM-authorized host identity; a deliberately unauthorized wrong-peer client must not become the runtime owner. If the suite intentionally launches multiple authorized daemon UIDs, grant KVM only to those explicit authorized UIDs and keep the wrong-peer UID distinct.

The wrong-peer probe currently fails at filesystem permission before peer-credential middleware; accept and assert this stronger OS-level refusal or make the socket traversable without weakening write/connect authorization, but do not treat it as a false failure.

Verification: logs/assertions pin daemon/child effective identities, child VM KVM opens, wrong peer rejected at OS or middleware, local and hosted-style E2E/full gates.
