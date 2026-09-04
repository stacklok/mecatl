package learning_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0259_AdmissionProvenanceBindsCurrentPromptAndRejectsForgery(t *testing.T) {
	t.Parallel()

	source := learning.AttemptSource{
		SessionID:       session.SessionID("session-7"),
		RunID:           learning.DurableRunID("run-42"),
		CanonicalDigest: learning.CanonicalDigest(strings.Repeat("a", 64)),
	}
	prompt := learning.CurrentPromptBinding{
		Ordinal: 3,
		Digest:  learning.CanonicalDigest(strings.Repeat("b", 64)),
		Origin:  learning.PromptOriginCurrentPrincipal,
	}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatalf("NewAdmissionProvenance() error = %v", err)
	}
	if err := provenance.Validate(source, prompt); err != nil {
		t.Fatalf("Validate(exact source) error = %v", err)
	}
	otherPrompt := prompt
	otherPrompt.Ordinal++
	if err := provenance.Validate(source, otherPrompt); !errors.Is(err, learning.ErrInvalidAttempt) {
		t.Fatalf("Validate(other current prompt) error = %v, want ErrInvalidAttempt", err)
	}

	for name, mutate := range map[string]func(*learning.AdmissionProvenance){
		"class": func(p *learning.AdmissionProvenance) {
			p.Class = learning.AdmissionWeighted
		},
		"session": func(p *learning.AdmissionProvenance) {
			p.Source.SessionID = session.SessionID("session-forged")
		},
		"run": func(p *learning.AdmissionProvenance) {
			p.Source.RunID = learning.DurableRunID("run-forged")
		},
		"canonical digest": func(p *learning.AdmissionProvenance) {
			p.Source.CanonicalDigest = learning.CanonicalDigest(strings.Repeat("c", 64))
		},
		"current prompt": func(p *learning.AdmissionProvenance) {
			p.CurrentPrompt.Digest = learning.CanonicalDigest(strings.Repeat("d", 64))
		},
		"prompt ordinal": func(p *learning.AdmissionProvenance) {
			p.CurrentPrompt.Ordinal++
		},
		"prompt origin": func(p *learning.AdmissionProvenance) {
			p.CurrentPrompt.Origin = learning.PromptOriginSynthetic
		},
		"seal": func(p *learning.AdmissionProvenance) {
			p.Binding = learning.ProvenanceBinding(strings.Repeat("e", 64))
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := provenance
			mutate(&forged)
			if err := forged.Validate(source, prompt); !errors.Is(err, learning.ErrInvalidAttempt) {
				t.Fatalf("forged provenance error = %v, want ErrInvalidAttempt", err)
			}
		})
	}

	synthetic := prompt
	synthetic.Origin = learning.PromptOriginSynthetic
	if _, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, synthetic); !errors.Is(err, learning.ErrInvalidAttempt) {
		t.Fatalf("synthetic continuation error = %v, want ErrInvalidAttempt", err)
	}

	idA, err := learning.DeterministicAttemptID("issuer\x00alice", source)
	if err != nil {
		t.Fatalf("DeterministicAttemptID(A) error = %v", err)
	}
	idB, err := learning.DeterministicAttemptID("issuer\x00bob", source)
	if err != nil {
		t.Fatalf("DeterministicAttemptID(B) error = %v", err)
	}
	if idA == idB || strings.Contains(string(idA), "alice") || strings.Contains(string(idA), string(source.SessionID)) {
		t.Fatalf("attempt identity does not safely partition callers and sources: A=%q B=%q", idA, idB)
	}
}

func TestADR_0259_AttemptSurfacesContainNoContentOrSecrets(t *testing.T) {
	t.Parallel()

	for _, typ := range []reflect.Type{
		reflect.TypeFor[learning.AttemptRecord](),
		reflect.TypeFor[learning.AttemptProjection](),
		reflect.TypeFor[learning.AttemptClaim](),
		reflect.TypeFor[learning.AdmissionProvenance](),
	} {
		if err := rejectUnsafeAttemptShape(typ, map[reflect.Type]bool{}); err != nil {
			t.Fatalf("%s is not content-free: %v", typ, err)
		}
	}

	// Prove the structural oracle itself rejects planted content leaks.
	for _, typ := range []reflect.Type{
		reflect.TypeFor[struct{ RawPrompt string }](),
		reflect.TypeFor[struct{ Note string }](),
	} {
		if err := rejectUnsafeAttemptShape(typ, map[reflect.Type]bool{}); err == nil {
			t.Fatalf("structural oracle accepted planted leak %s", typ)
		}
	}

	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	source := learning.AttemptSource{SessionID: "session-7", RunID: "run-42", CanonicalDigest: learning.CanonicalDigest(strings.Repeat("a", 64))}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionWeighted, source, learning.CurrentPromptBinding{
		Ordinal: 1, Digest: learning.CanonicalDigest(strings.Repeat("b", 64)), Origin: learning.PromptOriginCurrentPrincipal,
	})
	if err != nil {
		t.Fatalf("NewAdmissionProvenance() error = %v", err)
	}
	record := learning.AttemptRecord{
		ID:                learning.AttemptID("attempt-0123456789abcdef0123456789abcdef"),
		Version:           learning.AttemptVersion("v1"),
		State:             learning.AttemptFailed,
		Outcome:           learning.AttemptOutcomeFailed,
		FailureCode:       learning.FailureEvidenceUnavailable,
		Provenance:        provenance,
		AttemptGeneration: 2,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := learning.ValidateAttemptRecord(record); err != nil {
		t.Fatalf("ValidateAttemptRecord(valid) error = %v", err)
	}
	projection, err := learning.ProjectAttempt(record)
	if err != nil || projection.Source != source || projection.FailureCode != learning.FailureEvidenceUnavailable {
		t.Fatalf("ProjectAttempt() = %#v, %v; want exact safe source and failure code", projection, err)
	}

	record.FailureCode = learning.AttemptFailureCode("driver said: bearer secret-token")
	if err := learning.ValidateAttemptRecord(record); !errors.Is(err, learning.ErrInvalidAttempt) {
		t.Fatalf("open failure text error = %v, want ErrInvalidAttempt", err)
	}
}

func rejectUnsafeAttemptShape(typ reflect.Type, seen map[reflect.Type]bool) error {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
			return errors.New("byte content surface")
		}
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.String && typ.PkgPath() == "" {
		return errors.New("untyped string surface")
	}
	if typ.Kind() == reflect.Map || typ.Kind() == reflect.Interface {
		return errors.New("open metadata surface")
	}
	if seen[typ] {
		return nil
	}
	seen[typ] = true
	if typ.PkgPath() == "time" || typ.Kind() != reflect.Struct {
		return nil
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, forbidden := range []string{
			"content", "prompttext", "raw", "text", "body", "data", "args", "message", "detail", "reason", "error", "url",
			"path", "workspace", "principal", "owner", "actor", "issuer", "subject", "user", "credential", "apikey", "accesskey",
			"secret", "token", "password", "bearer", "cookie", "header", "drivererror", "diagnostic", "metric", "watch", "eventlog", "envelope",
		} {
			if strings.Contains(name, forbidden) {
				return errors.New("forbidden field " + field.Name)
			}
		}
		if err := rejectUnsafeAttemptShape(field.Type, seen); err != nil {
			return err
		}
	}
	return nil
}
