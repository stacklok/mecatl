package server_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestPresentScheduleNameUsesStoredOwnerProvenance(t *testing.T) {
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	digest := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	physical := fmt.Sprintf("schedule/%x\x00nightly cleanup", digest[:])

	got := server.PresentScheduleName(port.Schedule{Spec: port.ScheduleSpec{Name: physical, Owner: owner}})
	if got != "nightly cleanup" {
		t.Fatalf("PresentScheduleName(owned) = %q, want literal name", got)
	}
	if strings.ContainsRune(got, '\x00') || strings.Contains(got, fmt.Sprintf("%x", digest[:])) {
		t.Fatalf("literal name leaked physical namespace: %q", got)
	}
}

func TestPresentScheduleNamePreservesOwnerlessPhysicalLookingLiteral(t *testing.T) {
	digest := sha256.Sum256([]byte("https://issuer.example\x00alice"))
	literal := fmt.Sprintf("schedule/%x\x00nightly cleanup", digest[:])

	if got := server.PresentScheduleName(port.Schedule{Spec: port.ScheduleSpec{Name: literal}}); got != literal {
		t.Fatalf("PresentScheduleName(ownerless) = %q, want byte-exact literal %q", got, literal)
	}
	if got := server.PresentScheduleName(port.Schedule{Spec: port.ScheduleSpec{Name: "flat-name"}}); got != "flat-name" {
		t.Fatalf("ordinary ownerless name = %q, want unchanged", got)
	}
	const nearMiss = "schedule/not-a-digest\x00literal"
	if got := server.PresentScheduleName(port.Schedule{Spec: port.ScheduleSpec{Name: nearMiss}}); got != nearMiss {
		t.Fatalf("ownerless near-miss name = %q, want unchanged", got)
	}
}

func TestPresentScheduleNameRejectsMismatchedOwnedKeyWithoutLeak(t *testing.T) {
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bobDigest := sha256.Sum256([]byte("https://issuer.example\x00bob"))
	physical := fmt.Sprintf("schedule/%x\x00nightly cleanup", bobDigest[:])

	got := server.PresentScheduleName(port.Schedule{Spec: port.ScheduleSpec{Name: physical, Owner: alice}})
	if got == "nightly cleanup" || strings.Contains(got, physical) || strings.Contains(got, fmt.Sprintf("%x", bobDigest[:])) {
		t.Fatalf("mismatched owned key was trusted or leaked: %q", got)
	}
}
