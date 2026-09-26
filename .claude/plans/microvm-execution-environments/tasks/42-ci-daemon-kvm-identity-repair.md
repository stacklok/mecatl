---
id: 42-ci-daemon-kvm-identity-repair
title: Keep live microvmd on the KVM-authorized CI identity
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/42-ci-daemon-kvm-identity-repair"
worktree: ".scratch/task-microvm-42"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

Hosted runner can open `/dev/kvm` immediately before E2E, but microvmd doctor/preflight receives EACCES. Determine the actual daemon effective UID in the production E2E. Keep the primary daemon on the runner UID that owns the KVM ACL; exercise wrong-peer rejection by launching only the unauthorized client under an alternate UID, not the KVM-owning daemon. If a different cause is proven, fix that exact identity transition and pin it.

Verification: E2E logs daemon UID/KVM open identity, unauthorized peer still rejected, local and hosted-style Linux KVM journey pass, full/action gates pass.
