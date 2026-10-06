package mcpbroker

import (
	"strings"
	"testing"
	"time"
)

func TestSessionEnrollmentConstructorCompletedArm(t *testing.T) {
	cat, err := NewCatalogue(CatalogueRef(strings.Repeat("A", 43)), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := &EnrollmentStarted{Ref: EnrollmentRef(strings.Repeat("A", 43)), Prompt: BrowserPrompt{URL: "https://broker.test/authorize", ExpiresAt: time.Now().Add(time.Minute)}}
	for _, tc := range []struct {
		name      string
		kind      EnrollmentKind
		started   *EnrollmentStarted
		catalogue Catalogue
		valid     bool
	}{
		{"completed", EnrollmentCompletedKind, nil, cat, true},
		{"missing-catalogue", EnrollmentCompletedKind, nil, nil, false},
		{"conflicting-arms", EnrollmentCompletedKind, started, cat, false},
		{"started", EnrollmentStartedKind, started, nil, true},
		{"started-with-catalogue", EnrollmentStartedKind, started, cat, false},
		{"already-connected", EnrollmentAlreadyConnected, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NewBeginEnrollmentOutcome(tc.kind, tc.started, tc.catalogue)
			if (err == nil) != tc.valid {
				t.Fatalf("outcome=%+v error=%v", out, err)
			}
			if tc.valid && (out.Catalogue != tc.catalogue || !out.Valid()) {
				t.Fatal("constructor lost selected arm")
			}
		})
	}
}
