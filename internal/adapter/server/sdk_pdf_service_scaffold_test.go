package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestPDFArtifactServiceScaffoldFailsClosed(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPdf(context.Background(), sess.ID, "x.pdf", strings.NewReader("%PDF-1.4")); !errors.Is(err, server.ErrPDFArtifactsUnavailable) {
		t.Fatalf("upload without storage = %v, want unsupported-feature error", err)
	}
	if err := svc.DownloadPdf(context.Background(), sess.ID, "opaque-id"); !errors.Is(err, server.ErrPDFArtifactsUnavailable) {
		t.Fatalf("download without storage = %v, want unsupported-feature error", err)
	}
	if _, err := svc.UploadPdf(context.Background(), "unknown", "x.pdf", strings.NewReader("%PDF-1.4")); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("upload to unknown session = %v, want not-found", err)
	}
	if err := svc.DownloadPdf(context.Background(), "unknown", "opaque-id"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("download from unknown session = %v, want not-found", err)
	}
}
