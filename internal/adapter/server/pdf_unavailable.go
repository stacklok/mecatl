package server

import (
	"context"
	"errors"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// UploadPdf stages a private PDF only after the caller owns the exact session.
func (s *Service) UploadPdf(ctx context.Context, id session.SessionID, name string, source io.Reader) (PDFArtifact, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return PDFArtifact{}, err
	}
	if s.cfg.PDFArtifacts == nil {
		return PDFArtifact{}, ErrPDFArtifactsUnavailable
	}
	artifact, err := s.cfg.PDFArtifacts.Stage(ctx, id, name, source)
	if err == nil {
		return artifact, nil
	}
	if ctx.Err() != nil {
		return PDFArtifact{}, ctx.Err()
	}
	if errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrNotFound) {
		return PDFArtifact{}, err
	}
	return PDFArtifact{}, ErrInternal
}

// DownloadPdf is the fail-closed service seam until PDF artifact storage is
// composed. The later download task replaces the body and returns a reader.
func (s *Service) DownloadPdf(ctx context.Context, id session.SessionID, _ string) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	return ErrPDFArtifactsUnavailable
}
