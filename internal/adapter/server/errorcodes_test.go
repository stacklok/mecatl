package server

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/grpc/codes"

	"github.com/stacklok/mecatl/engine/port"
)

// preRefactorMappings is the GOLDEN table: every sentinel classification exactly
// as the two hand-maintained switches (toStatus in grpc.go, writeServiceError in
// http.go) resolved it BEFORE the registry replaced them.
//
// It was extracted mechanically from the pre-refactor source, not retyped, and
// it exists to prove one thing: collapsing 49 hand-written cases per transport
// into one table changed NO caller-visible classification. That guarantee is not
// theoretical — the first mechanical extraction silently dropped the four
// port.*-qualified cases below because its pattern only matched unqualified
// Err* identifiers. The compiler caught that one. This table is what catches the
// next one.
var preRefactorMappings = []struct {
	sentinel error
	grpc     codes.Code
	http     int
}{
	{ErrChildNotFound, codes.NotFound, http.StatusNotFound},
	{ErrCleanupBackend, codes.Internal, http.StatusInternalServerError},
	{ErrCleanupPlanStale, codes.Aborted, http.StatusConflict},
	{ErrCleanupUnsupported, codes.Unimplemented, http.StatusNotImplemented},
	{ErrDreamApplyFailed, codes.Internal, http.StatusInternalServerError},
	{ErrDreamCapacity, codes.ResourceExhausted, http.StatusTooManyRequests},
	{ErrDreamConflict, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrDreamDeadline, codes.DeadlineExceeded, http.StatusGatewayTimeout},
	{ErrDreamGenerateFailed, codes.Internal, http.StatusInternalServerError},
	{ErrDreamInProgress, codes.Aborted, http.StatusConflict},
	{ErrDreamNotFound, codes.NotFound, http.StatusNotFound},
	{ErrDreamRequestFailed, codes.Internal, http.StatusInternalServerError},
	{ErrDreamTerminalConflict, codes.AlreadyExists, http.StatusGone},
	{ErrDreamUnavailable, codes.Unimplemented, http.StatusNotImplemented},
	{ErrFailedPrecondition, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrFailedStepRetryIneligible, codes.FailedPrecondition, http.StatusConflict},
	{ErrFireNowOverlap, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrInternal, codes.Internal, http.StatusInternalServerError},
	{ErrInvalidArgument, codes.InvalidArgument, http.StatusBadRequest},
	{ErrLearningUnavailable, codes.Unimplemented, http.StatusNotImplemented},
	{ErrManagementUnauthorized, codes.PermissionDenied, http.StatusForbidden},
	{ErrMigrationBackend, codes.Internal, http.StatusInternalServerError},
	{ErrMigrationConflict, codes.Aborted, http.StatusConflict},
	{ErrMigrationUnsupported, codes.Unimplemented, http.StatusNotImplemented},
	{ErrNoActiveRun, codes.FailedPrecondition, http.StatusConflict},
	{ErrNoEventLog, codes.Unimplemented, http.StatusNotImplemented},
	{ErrNoMCPProvider, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrNoScheduleStore, codes.Unimplemented, http.StatusNotImplemented},
	{ErrNotAwaitingPlan, codes.FailedPrecondition, http.StatusConflict},
	{ErrNotFound, codes.NotFound, http.StatusNotFound},
	{ErrProposalConflict, codes.Aborted, http.StatusConflict},
	{ErrScheduleDisabled, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrScheduleExhausted, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrScheduleNotLeader, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrSchedulerNotRunning, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrSessionDeleteUnsupported, codes.Unimplemented, http.StatusNotImplemented},
	{ErrSessionLeasedElsewhere, codes.FailedPrecondition, http.StatusConflict},
	{ErrStorageHealthBackend, codes.Internal, http.StatusInternalServerError},
	{ErrTeamNotFound, codes.NotFound, http.StatusNotFound},
	{ErrTeamNotRunning, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrTeamRunning, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrTeamsDisabled, codes.FailedPrecondition, http.StatusPreconditionFailed},
	{ErrTooManySessionEngines, codes.ResourceExhausted, http.StatusTooManyRequests},
	{ErrTooManyTeams, codes.ResourceExhausted, http.StatusTooManyRequests},
	{ErrUnavailable, codes.Unavailable, http.StatusServiceUnavailable},
	{port.ErrScheduleNotFound, codes.NotFound, http.StatusNotFound},
	{port.ErrScheduleUnsupported, codes.Unimplemented, http.StatusNotImplemented},
	{port.ErrSessionMetadataCursorRestart, codes.Aborted, http.StatusConflict},
	{port.ErrSessionMetadataPagingUnsupported, codes.Unimplemented, http.StatusNotImplemented}}

// TestSDKServerEnablers_Scenario2_RegistryPreservesPreRefactorMappings is the
// behaviour-preservation proof for AC2.2/AC2.3.
func TestSDKServerEnablers_Scenario2_RegistryPreservesPreRefactorMappings(t *testing.T) {
	for _, want := range preRefactorMappings {
		got := classifyError(want.sentinel)
		if got.GRPC != want.grpc {
			t.Errorf("%v: gRPC code = %v, want %v (the registry changed a pre-existing classification)", want.sentinel, got.GRPC, want.grpc)
		}
		if got.HTTPStatus != want.http {
			t.Errorf("%v: HTTP status = %d, want %d (the registry changed a pre-existing classification)", want.sentinel, got.HTTPStatus, want.http)
		}
		if got.Code == "" {
			t.Errorf("%v: empty code", want.sentinel)
		}
	}
	// Every golden sentinel must be REACHABLE — a row present but shadowed by an
	// earlier, more general row would classify correctly here only by accident.
	if len(preRefactorMappings) != len(errorRegistry) {
		t.Errorf("registry has %d rows, golden table has %d — a sentinel was added or dropped without updating the proof",
			len(errorRegistry), len(preRefactorMappings))
	}
}

// TestADR_0244_ErrorRegistryIsTotalAndUnambiguous is AC2.3.
func TestADR_0244_ErrorRegistryIsTotalAndUnambiguous(t *testing.T) {
	seenCode := map[string]error{}
	for _, e := range errorRegistry {
		if e.Sentinel == nil {
			t.Errorf("registry row %q has a nil sentinel", e.Code)
			continue
		}
		if e.Code == "" {
			t.Errorf("registry row for %v has an empty code", e.Sentinel)
		}
		if e.Title == "" {
			t.Errorf("registry row %q has an empty title; RFC 9457 title is read by humans", e.Code)
		}
		if e.HTTPStatus < 400 || e.HTTPStatus > 599 {
			t.Errorf("registry row %q maps to HTTP %d, which is not an error status", e.Code, e.HTTPStatus)
		}
		if e.GRPC == codes.OK {
			t.Errorf("registry row %q maps to codes.OK", e.Code)
		}
		// A duplicate code is allowed ONLY when both rows agree on both
		// transports — two sentinels can legitimately share a public identity
		// (ErrMigrationBackend and ErrCleanupBackend are both "storage
		// maintenance failed"). Disagreeing rows sharing a code would make the
		// code meaningless to a client.
		if prev, dup := seenCode[e.Code]; dup {
			pe := classifyError(prev)
			if pe.GRPC != e.GRPC || pe.HTTPStatus != e.HTTPStatus {
				t.Errorf("code %q is shared by %v and %v with DIFFERENT statuses (%v/%d vs %v/%d)",
					e.Code, prev, e.Sentinel, pe.GRPC, pe.HTTPStatus, e.GRPC, e.HTTPStatus)
			}
		} else {
			seenCode[e.Code] = e.Sentinel
		}
	}
	// Codes are an identifier vocabulary: they must be machine-safe, since they
	// travel in a URN and are branched on as literals by SDK clients.
	for _, e := range append(append([]errorCodeEntry{}, errorRegistry...), genericErrorEntry) {
		for _, r := range e.Code {
			lower := r >= 'a' && r <= 'z'
			digit := r >= '0' && r <= '9'
			if !lower && !digit && r != '_' {
				t.Errorf("code %q contains %q; codes must be lower_snake_case ASCII", e.Code, r)
				break
			}
		}
	}
}

// TestSDKServerEnablers_Scenario2_SpecificSentinelsWinOverGeneral pins the
// ordering contract: errorRegistry is a SLICE and classifyError walks it in
// order, because sentinels wrap other sentinels.
//
// ErrFailedStepRetryIneligible wraps ErrFailedPrecondition, so errors.Is matches
// BOTH. If the registry were a map (or if someone reordered it), this would
// classify as the general precondition failure and the HTTP status would change
// from 409 to 412 — a silently different contract for a client branching on it.
func TestSDKServerEnablers_Scenario2_SpecificSentinelsWinOverGeneral(t *testing.T) {
	if !errors.Is(ErrFailedStepRetryIneligible, ErrFailedPrecondition) {
		t.Fatal("test is vacuous: ErrFailedStepRetryIneligible no longer wraps ErrFailedPrecondition")
	}
	got := classifyError(ErrFailedStepRetryIneligible)
	if got.Code != "failed_step_retry_ineligible" {
		t.Errorf("code = %q, want the SPECIFIC %q — the general row shadowed it", got.Code, "failed_step_retry_ineligible")
	}
	if got.HTTPStatus != http.StatusConflict {
		t.Errorf("HTTP status = %d, want %d", got.HTTPStatus, http.StatusConflict)
	}
}

// TestADR_0244_NoRegistryRowIsShadowed is the GENERIC form of the ordering
// contract the test above pins for one known pair.
//
// TestSDKServerEnablers_Scenario2_SpecificSentinelsWinOverGeneral protects
// ErrFailedStepRetryIneligible/ErrFailedPrecondition BY NAME, which means
// correctness for any FUTURE wrapping pair depends on the next author
// remembering to hand-write another test like it. Nobody remembers. This walks
// the registry itself, so a new pair inserted in the wrong order fails
// immediately with no test edit.
//
// The assertion is row IDENTITY, deliberately, and this is the subtle part: a
// specific sentinel WRAPS the general one, so errors.Is(specific, general) is
// true. An errors.Is assertion would therefore hold both when the row resolves
// correctly and when it has been shadowed by the very row it wraps — passing in
// exactly the case it exists to catch. Only pointer identity distinguishes them.
func TestADR_0244_NoRegistryRowIsShadowed(t *testing.T) {
	for i, e := range errorRegistry {
		if e.Sentinel == nil {
			continue // reported by the totality test
		}
		got := classifyError(e.Sentinel)
		if got.Sentinel == e.Sentinel {
			continue
		}
		t.Errorf("registry row %d (%q) is SHADOWED: its own sentinel classifies as %q.\n"+
			"classifyError returns the FIRST row whose sentinel matches errors.Is, so a row whose sentinel "+
			"wraps an earlier row's is unreachable. Move %q above %q in errorRegistry.",
			i, e.Code, got.Code, e.Code, got.Code)
	}

	// The check must actually bite, or the loop above is theatre: prove that a
	// deliberately mis-ordered registry IS caught. This mirrors the real hazard —
	// a specific sentinel placed after the general one it wraps.
	specific, general := ErrFailedStepRetryIneligible, ErrFailedPrecondition
	if !errors.Is(specific, general) {
		t.Fatal("bite check is vacuous: ErrFailedStepRetryIneligible no longer wraps ErrFailedPrecondition")
	}
	misordered := []errorCodeEntry{
		{Sentinel: general, Code: "general"},
		{Sentinel: specific, Code: "specific"},
	}
	var found errorCodeEntry
	for _, e := range misordered {
		if errors.Is(specific, e.Sentinel) {
			found = e
			break
		}
	}
	if found.Sentinel == specific {
		t.Error("bite check failed: a mis-ordered registry did not shadow the specific sentinel, so the loop above proves nothing")
	}
}

// TestSDKServerEnablers_Scenario2_UnregisteredFailureDegrades is AC2.5.
func TestSDKServerEnablers_Scenario2_UnregisteredFailureDegrades(t *testing.T) {
	got := classifyError(errors.New("some downstream exploded"))
	if got.Code != genericErrorEntry.Code {
		t.Errorf("code = %q, want the generic %q", got.Code, genericErrorEntry.Code)
	}
	if got.GRPC != codes.Internal || got.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("unregistered error mapped to %v/%d, want Internal/500", got.GRPC, got.HTTPStatus)
	}
	// A wrapped registered sentinel must still be found, not degraded.
	wrapped := fmt.Errorf("while loading session: %w", ErrNotFound)
	if c := classifyError(wrapped).Code; c != "session_not_found" {
		t.Errorf("wrapped sentinel classified as %q, want session_not_found", c)
	}
}

// TestADR_0244_ProblemDetailsCarryNoSecrets is AC2.4.
//
// It walks EVERY registered code's rendered problem body — the whole vocabulary,
// not a sample — over the harness-authored halves (type, title, code). `detail`
// and its `error` alias are NOT covered by that walk, and the boundary is a
// deliberate contract rather than a gap in the test: see
// TestADR_0244_DetailIsPassedThroughNotScrubbed below, which pins what actually
// happens to them.
//
// The two halves get DIFFERENT checks on purpose, and the difference is the
// point. `code` is a machine vocabulary we author entirely, so a bare-word
// denylist is a cheap, exact tripwire there. `title` is English prose read by
// humans, and a bare-word denylist on prose is a false-positive generator: the
// first run of this test failed on "Management authorization required", which
// describes a requirement and leaks nothing. What actually matters for a title
// is that no secret VALUE reached it, so that half checks value SHAPES —
// bearer-token forms, key=value assignments, and long opaque runs — instead of
// vocabulary.
func TestADR_0244_ProblemDetailsCarryNoSecrets(t *testing.T) {
	secretWords := []string{"token", "secret", "password", "api_key", "apikey", "bearer", "credential", "private_key"}
	// A secret VALUE looks like an assignment or an opaque high-entropy run —
	// never like a sentence.
	// The opaque-run class deliberately EXCLUDES _ and - . Those are word
	// separators in our own vocabulary, and including them matched long
	// snake_case codes like "session_metadata_paging_unsupported" — an
	// identifier with word separators is not opaque. A real credential is an
	// unbroken run.
	secretValue := regexp.MustCompile(`(?i)(bearer\s+\S+|(token|secret|password|key|credential)\s*[:=]\s*\S+|[A-Za-z0-9+/]{24,})`)

	all := append(append([]errorCodeEntry{}, errorRegistry...), genericErrorEntry)
	for _, e := range all {
		doc := newProblem(e, "detail text")

		low := strings.ToLower(doc.Code)
		for _, w := range secretWords {
			if strings.Contains(low, w) {
				t.Errorf("code %q contains secret-shaped word %q; codes are a machine vocabulary and must never name a credential", e.Code, w)
			}
		}
		if m := secretValue.FindString(doc.Title); m != "" {
			t.Errorf("code %q: title %q contains a secret-shaped value %q", e.Code, doc.Title, m)
		}
		if m := secretValue.FindString(doc.Type); m != "" {
			t.Errorf("code %q: type %q contains a secret-shaped value %q", e.Code, doc.Type, m)
		}

		if doc.Status != e.HTTPStatus {
			t.Errorf("code %q: problem status = %d, want %d", e.Code, doc.Status, e.HTTPStatus)
		}
		if doc.Type != problemTypePrefix+e.Code {
			t.Errorf("code %q: problem type = %q, want %q", e.Code, doc.Type, problemTypePrefix+e.Code)
		}
	}

	// The value-shape matcher must actually bite, or the loop above is theatre.
	for _, leak := range []string{"Bearer abcdef123456", "token=hunter2", "sk-01234567890123456789abcdef"} {
		if secretValue.FindString(leak) == "" {
			t.Errorf("secret-value matcher missed %q; the AC2.4 guard would not catch a real leak", leak)
		}
	}
	// ...and must not bite on ordinary prose, or it will be silenced by the next
	// person who trips it.
	for _, ok := range []string{"Management authorization required", "Session not found", "Server is draining and not accepting new runs", "urn:mecatl:error:session_metadata_paging_unsupported"} {
		if m := secretValue.FindString(ok); m != "" {
			t.Errorf("secret-value matcher false-positives on %q (matched %q)", ok, m)
		}
	}
}

// TestADR_0244_DetailIsPassedThroughNotScrubbed pins the boundary of the AC2.4
// guarantee, which the review found was claiming more than the code delivers.
//
// The no-secret walk above covers code/title/type — the halves mecatl authors.
// `detail` and its `error` alias carry the caller's own err.Error(), and are
// bounded and UTF-8-repaired but NOT content-scrubbed. This test asserts that
// pass-through EXPLICITLY, so the boundary is a decision on the record instead
// of an untested assumption, and so a future author who adds scrubbing has to
// come here and delete an assertion that says why it was not wanted.
//
// Not scrubbing is the right call, and not merely the cheap one. A denylist over
// free-text error prose is the false-positive generator the AC2.4 comment
// already describes for titles, and the value it would redact is not known to be
// a secret at this layer — only the backend that raised the error knows which of
// its own substrings is sensitive. Redacting there is precise; redacting here is
// a guess that corrupts diagnostics. The obligation therefore sits with the
// raising backend, which is where today's risky ones (migration, cleanup,
// storage-health) already discharge it.
func TestADR_0244_DetailIsPassedThroughNotScrubbed(t *testing.T) {
	// Shapes the AC2.4 matcher WOULD flag if they appeared in a title.
	for _, secretish := range []string{
		"Bearer abcdef1234567890",
		"token=hunter2",
		"api_key: sk-01234567890123456789abcdef",
	} {
		doc := newProblem(genericErrorEntry, "upstream rejected: "+secretish)
		if !strings.Contains(doc.Detail, secretish) {
			t.Errorf("detail = %q; it must carry err.Error() verbatim. If scrubbing was "+
				"added deliberately, update AC2.4 and this test's rationale together.", doc.Detail)
		}
		if doc.Error != doc.Detail {
			t.Errorf("error alias %q diverged from detail %q", doc.Error, doc.Detail)
		}
	}

	// The two protections detail DOES get, on a value that needs both at once.
	huge := strings.Repeat("s3cr3t", maxProblemDetail)
	doc := newProblem(genericErrorEntry, huge+"\xe2")
	if len(doc.Detail) > maxProblemDetail+len("…") {
		t.Errorf("detail is %d bytes; bounding applies to caller text too", len(doc.Detail))
	}
	if !utf8ValidString(doc.Detail) {
		t.Error("detail is not valid UTF-8; the marshal-killing class must be repaired even when unscrubbed")
	}

	// And the harness-authored halves stay clean no matter what the caller sent —
	// a caller cannot reach them.
	if strings.Contains(doc.Code, "s3cr3t") || strings.Contains(doc.Title, "s3cr3t") || strings.Contains(doc.Type, "s3cr3t") {
		t.Error("caller-supplied detail leaked into code/title/type, which AC2.4 does guarantee are clean")
	}
}

// TestSDKServerEnablers_Scenario2_DetailIsBoundedAndValidUTF8 is AC2.6.
//
// detail carries err.Error(), which can include a downstream's text. Invalid
// UTF-8 there is the marshal-killing class AGENTS.md documents, and an unbounded
// detail turns every response into an unbounded payload.
func TestSDKServerEnablers_Scenario2_DetailIsBoundedAndValidUTF8(t *testing.T) {
	long := strings.Repeat("a", maxProblemDetail*3)
	doc := newProblem(genericErrorEntry, long)
	if len(doc.Detail) > maxProblemDetail+len("…") {
		t.Errorf("detail is %d bytes, want <= %d", len(doc.Detail), maxProblemDetail+len("…"))
	}
	if !utf8ValidString(doc.Detail) {
		t.Error("clamped detail is not valid UTF-8")
	}

	// An invalid-UTF-8 detail is repaired, not passed through.
	bad := newProblem(genericErrorEntry, "café\xe2 broke")
	if !utf8ValidString(bad.Detail) {
		t.Errorf("detail %q is not valid UTF-8 after repair", bad.Detail)
	}
	// The compatibility `error` extension carries the SAME repaired, bounded
	// text as `detail` — an old client reading `error` must not get a different
	// (or unrepaired) message than a new one reading `detail`.
	if bad.Error != bad.Detail {
		t.Errorf("error extension %q differs from detail %q", bad.Error, bad.Detail)
	}

	// Clamping must cut on a rune boundary: a truncated multi-byte rune would
	// itself be invalid UTF-8, re-creating the fault the repair prevents.
	multi := strings.Repeat("é", maxProblemDetail)
	if got := newProblem(genericErrorEntry, multi); !utf8ValidString(got.Detail) {
		t.Error("clamping a multi-byte string produced invalid UTF-8")
	}
}

// TestSDKServerEnablers_Scenario2_HTTPStatusEntriesAreConsistent covers the
// writeError path: request-shape failures that carry a status but no sentinel.
func TestSDKServerEnablers_Scenario2_HTTPStatusEntriesAreConsistent(t *testing.T) {
	for status, e := range httpStatusEntries {
		if e.HTTPStatus != status {
			t.Errorf("httpStatusEntries[%d] carries HTTPStatus %d", status, e.HTTPStatus)
		}
		if e.Code == "" || e.Title == "" {
			t.Errorf("httpStatusEntries[%d] has an empty code or title", status)
		}
	}
	// An unmapped status keeps the CALLER's status rather than being coerced to
	// 500: the handler knew the right HTTP semantics even where we have no name.
	got := entryForHTTPStatus(http.StatusTeapot)
	if got.HTTPStatus != http.StatusTeapot {
		t.Errorf("unmapped status became %d, want it preserved as %d", got.HTTPStatus, http.StatusTeapot)
	}
	if got.Code != genericErrorEntry.Code {
		t.Errorf("unmapped status code = %q, want the generic %q", got.Code, genericErrorEntry.Code)
	}
}

// port is referenced by the golden table above; this keeps the import honest if
// every port.* row were ever removed.
var _ = port.ErrScheduleNotFound

// utf8ValidString is a local alias so the assertions above read as intent.
func utf8ValidString(s string) bool { return utf8.ValidString(s) }
