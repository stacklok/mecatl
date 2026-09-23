package server

import (
	"testing"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestToProtoResultTypedMetadataPresence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		d     session.RetryDisposition
		p     session.StreamProgress
		wantD mecatlv1.RetryDisposition
		wantP mecatlv1.StreamProgress
	}{
		{"explicit unknown", session.RetryDispositionUnknown, session.StreamProgressUnknown, mecatlv1.RetryDisposition_RETRY_DISPOSITION_UNKNOWN, mecatlv1.StreamProgress_STREAM_PROGRESS_UNKNOWN},
		{"retryable visible", session.RetryDispositionRetryable, session.StreamProgressVisible, mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE, mecatlv1.StreamProgress_STREAM_PROGRESS_VISIBLE},
		{"permanent complete", session.RetryDispositionPermanent, session.StreamProgressComplete, mecatlv1.RetryDisposition_RETRY_DISPOSITION_PERMANENT, mecatlv1.StreamProgress_STREAM_PROGRESS_COMPLETE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toProtoResult(session.ResultPayload{Stop: session.StopError, Disposition: tc.d, Progress: tc.p})
			if got.RetryDisposition == nil || *got.RetryDisposition != tc.wantD {
				t.Fatalf("retry disposition = %v", got.RetryDisposition)
			}
			if got.StreamProgress == nil || *got.StreamProgress != tc.wantP {
				t.Fatalf("stream progress = %v", got.StreamProgress)
			}
		})
	}
}

func TestResultOptionalRetryMetadataPresenceRoundTrip(t *testing.T) {
	retryField := (&mecatlv1.Result{}).ProtoReflect().Descriptor().Fields().ByName("retry_disposition")
	progressField := (&mecatlv1.Result{}).ProtoReflect().Descriptor().Fields().ByName("stream_progress")

	legacy := &mecatlv1.Result{Stop: string(session.StopError)}
	if legacy.ProtoReflect().Has(retryField) || legacy.ProtoReflect().Has(progressField) {
		t.Fatal("old result unexpectedly has optional retry metadata")
	}
	legacyWire, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyRoundTrip mecatlv1.Result
	if err := proto.Unmarshal(legacyWire, &legacyRoundTrip); err != nil {
		t.Fatal(err)
	}
	if legacyRoundTrip.ProtoReflect().Has(retryField) || legacyRoundTrip.ProtoReflect().Has(progressField) {
		t.Fatal("absent optional retry metadata became present after round trip")
	}

	explicit := toProtoResult(session.ResultPayload{Stop: session.StopError})
	if !explicit.ProtoReflect().Has(retryField) || !explicit.ProtoReflect().Has(progressField) {
		t.Fatal("new explicit unknown retry metadata is absent")
	}
	if explicit.GetRetryDisposition() != mecatlv1.RetryDisposition_RETRY_DISPOSITION_UNKNOWN || explicit.GetStreamProgress() != mecatlv1.StreamProgress_STREAM_PROGRESS_UNKNOWN {
		t.Fatalf("explicit metadata = (%v,%v), want unknown/unknown", explicit.GetRetryDisposition(), explicit.GetStreamProgress())
	}
	explicitWire, err := proto.Marshal(explicit)
	if err != nil {
		t.Fatal(err)
	}
	var explicitRoundTrip mecatlv1.Result
	if err := proto.Unmarshal(explicitWire, &explicitRoundTrip); err != nil {
		t.Fatal(err)
	}
	if !explicitRoundTrip.ProtoReflect().Has(retryField) || !explicitRoundTrip.ProtoReflect().Has(progressField) {
		t.Fatal("explicit unknown retry metadata lost presence after round trip")
	}
	if explicitRoundTrip.GetRetryDisposition() != mecatlv1.RetryDisposition_RETRY_DISPOSITION_UNKNOWN || explicitRoundTrip.GetStreamProgress() != mecatlv1.StreamProgress_STREAM_PROGRESS_UNKNOWN {
		t.Fatalf("round-trip metadata = (%v,%v), want unknown/unknown", explicitRoundTrip.GetRetryDisposition(), explicitRoundTrip.GetStreamProgress())
	}
}
