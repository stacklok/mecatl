package learning

import (
	"errors"
	"testing"
	"time"
)

func TestADR_0254_AutomaticAdmissionReservationContract(t *testing.T) {
	t.Parallel()

	attempt := AttemptID("attempt-0123456789abcdef")
	first, err := AutomaticReservationIDForAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AutomaticReservationIDForAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == "" {
		t.Fatalf("reservation id is not deterministic: first=%q second=%q", first, second)
	}
	if _, err = AutomaticReservationIDForAttempt("not-an-attempt"); !errors.Is(err, ErrInvalidAutomaticReservation) {
		t.Fatalf("invalid attempt id error = %v, want ErrInvalidAutomaticReservation", err)
	}

	policy := AutomaticAdmissionPolicy{
		Window:                   time.Hour,
		Cooldown:                 time.Minute,
		DedupeWindow:             24 * time.Hour,
		MaxCount:                 4,
		MaxTokens:                400,
		MaxCountPerPrincipal:     2,
		MaxTokensPerPrincipal:    200,
		ReservationClaimDuration: 5 * time.Minute,
	}
	if err = policy.Validate(); err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	invalidDedupe := policy
	invalidDedupe.DedupeWindow = policy.Window - time.Second
	if err = invalidDedupe.Validate(); !errors.Is(err, ErrInvalidAutomaticReservation) {
		t.Fatalf("short dedupe window error = %v, want ErrInvalidAutomaticReservation", err)
	}

	revision, err := AutomaticAdmissionPolicyRevisionFor(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []AdmissionClass{AdmissionWeighted, AdmissionHard} {
		req := AutomaticReservationRequest{
			ID:                     first,
			AttemptID:              attempt,
			Principal:              AttemptPartition("principal-partition"),
			Digest:                 CanonicalDigest("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
			Class:                  class,
			Tokens:                 100,
			ExpectedPolicyRevision: revision,
		}
		if err = req.Validate(); err != nil {
			t.Fatalf("%s request: %v", class, err)
		}
	}

	host := AutomaticReservationRequest{ID: first, AttemptID: attempt, Principal: "principal-partition", Digest: "0123456789abcdef", Class: AdmissionHostRequested, Tokens: 100, ExpectedPolicyRevision: revision}
	if err = host.Validate(); !errors.Is(err, ErrInvalidAutomaticReservation) {
		t.Fatalf("host-requested automatic reservation error = %v, want ErrInvalidAutomaticReservation", err)
	}

	fence := AutomaticReservationFence{Generation: 1, ExpiresAt: time.Now().Add(time.Minute)}
	if !fence.ValidAt(time.Now()) {
		t.Fatal("live reservation fence reported invalid")
	}
}
