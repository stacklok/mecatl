package server

import (
	"context"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// UploadPdf is the fail-closed service seam until PDF artifact storage is
// composed. The later storage task replaces the body and returns metadata.
func (s *Service) UploadPdf(ctx context.Context, id session.SessionID, _ string, _ io.Reader) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	return ErrPDFArtifactsUnavailable
}

// DownloadPdf is the fail-closed service seam until PDF artifact storage is
// composed. The later download task replaces the body and returns a reader.
func (s *Service) DownloadPdf(ctx context.Context, id session.SessionID, _ string) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	return ErrPDFArtifactsUnavailable
}
