# Contextual guardrail private-capacity evidence

Date: 2026-09-15

This report supports AC6.5 of the [contextual guardrails acceptance plan](../acceptance/contextual-guardrails.md). It records implementation calibration, not a public configuration contract or a checker-model efficacy claim. All workloads were synthetic and offline; no production content, provider route, model call, token price, or invented dollar estimate was used.

## Selected finite capacities

| Resource | Selected capacity | Unit and rationale |
|---|---:|---|
| review evidence | 16 opaque page handles; 400,000 aggregate source bytes | Each page is at most the native Read-shaped 25,000-byte / 2,000-line preview. A larger authorized textual source consumes a finite same-version continuation chain; capacity exhaustion or a version change leaves evidence incomplete. Sixteen full ASCII pages are about 100k tokens at four bytes/token, leaving space beneath the engine's 128k unknown-model floor for the fixed rubric, call, trajectory, and answer. This is workload calibration, not a universal byte-to-token guarantee. |
| root trajectory | 256 facts; 128,000 retained content bytes | The stress fixture uses 300-byte target identities plus direction/class/decision metadata, representing a long tool/delegation run. Exact duplicates allocate no retained fact. The count is not an arbitrary model-read limit; it is root-run metadata retention, and exhaustion sticks the ledger incomplete. |
| held results | 32 results; 2,097,152 aggregate content bytes | Thirty-two is four times the default maximum concurrent child fan-out and covers a large read batch without turning escrow into durable storage. Two MiB accommodates 32 synthetic 64-KiB results. Admission checks aggregate size before cloning parts; overflow takes the existing synthetic withholding path. |
| transient detail | 4,096 entries; 16,777,216 retained bytes | The process registry can retain multiple concurrent roots while each detail input field is pre-clamped to 4,096 bytes and each rendered field to 1,000 runes. Entry and byte admission are checked before map insertion. Root cleanup and Service shutdown remove retained state. |
| exact repeat grants | 1,024 sessions; 256 digests/session | Existing process/session bounds in the keyed grant holder. Full capacity disables “Don't ask again” for a new scope while preserving Run once; session clear and restart remove grants. |

The production definitions are `internal/app/contextual_reviewer.go` (`maxReviewEvidenceHandles`, `maxReviewEvidenceBytes`, `maxReviewEvidenceRead`, `maxReviewEvidenceLines`), `engine/agent/actionreview.go` (`defaultReviewTrajectoryFacts`, `defaultReviewTrajectoryBytes`), `engine/agent/inboundreview.go` (`maxHeldResults`, `maxHeldResultBytes`), `internal/adapter/server/guardrails_status.go` (`maxReviewDetailEntries`, `maxReviewDetailRegistryBytes`), and `internal/adapter/modelhook/waiver.go` (`maxContextualGrantSessions`, `maxContextualGrantsPerSession`).

## Offline experiments

Commands ran on a 13th Gen Intel Core i7-1365U, linux/amd64, with `-benchtime=20x -count=3`. Nanoseconds are advisory machine-local evidence; retained byte/count assertions are deterministic.

```text
cd engine && go test ./agent -run '^$' -bench BenchmarkContextualReviewPrivateCapacity -benchmem -benchtime=20x -count=3
trajectory-exhaustion: 397423–566034 ns/op, 178794–178806 B/op, 784 allocs/op
held-result-fill-clear: 19098–24784 ns/op, 23018 B/op, 74 allocs/op

go test ./internal/app -run '^$' -bench BenchmarkContextualReviewEvidenceCapacity -benchmem -benchtime=20x -count=3
evidence-capacity: 11696–17197 ns/op, 10056 B/op, 105 allocs/op
```

The evidence benchmark admits sixteen 25,000-byte bounded backends, rejects the seventeenth, and asserts zero body reads when metadata alone proves capacity exhaustion. It therefore measures inventory allocation rather than silently allocating a full `Workspace.ReadVersion` body and truncating it. Production workspace evidence uses bounded same-snapshot range reads to derive finite byte/line page boundaries; a backend without the bounded versioned capability is incomplete and receives no path-based handle.

The trajectory benchmark drives one fact beyond both finite production constraints and requires `TrajectoryComplete=false`. The held-result benchmark fills all 32 slots with 64-KiB synthetic results and clears the map. The race proof concurrently invokes cancellation cleanup and requires zero retained entries/bytes. The transient-detail stress fills the finite registry with pre-clamped synthetic detail, rejects the next insertion without changing retained count/bytes, then verifies root cleanup reaches zero.

## Executable proof split

AC6.4 is executable production behavior, not a prose assertion:

- `internal/app/contextual_reviewer_protocol_test.go` (`TestADR_0363_ContextualGuardrails_Scenario2_CapacityBeforeAllocation`) proves an oversized evidence read is rejected before backend access.
- `internal/app/contextual_capacity_task6_test.go` (`TestContextualReviewEvidenceCalibrationBeforeAllocation`) proves aggregate handle/byte exhaustion performs no body read and cannot validate as acceptable.
- `engine/agent/contextual_capacity_task6_internal_test.go` (`TestADR_0363_ContextualGuardrails_Scenario6_ImplementationCalibration`) checks the selected production capacities, trajectory incomplete behavior, held-result pre-clone rejection, and race-clean cancellation cleanup.
- `internal/adapter/server/contextual_guardrails_task5_test.go` (`TestReviewDetailRegistryCapacityAndCleanup`) checks finite transient-detail admission and cleanup.

AC6.5 is the separate human inspection of this report: selected values and units, rationale, experiment output, and exact executable artifacts. The paired corpus and report protocol live in `internal/app/testdata/contextual_guardrails_corpus.v1.json`, `internal/app/testdata/contextual_guardrails_baseline.v1.json`, and `internal/adapter/guardraileval/eval.go`. Offline fixture outcomes explicitly set `efficacy_established=false`; a live runner refuses execution unless its caller supplies both an explicit route and operator spend authorization. No live comparison was run here.
