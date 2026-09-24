package server

import (
	"context"
	"errors"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// UploadPdf stages a private PDF only after the caller owns the exact session.
func (s *Service) UploadPdf(ctx context.Context, id session.SessionID, name string, source io.Reader) (PDFArtifact, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return PDFArtifact{}, err
	}
	if s.cfg.PDFArtifacts == nil {
		return PDFArtifact{}, ErrPDFArtifactsUnavailable
	}
	if !s.sessionCapabilitiesFor(sess).PDF {
		return PDFArtifact{}, ErrInvalidArgument
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

// DownloadPdf authorizes the exact persisted session before opening any PDF
// object. A caller owns the returned reader and must close it.
func (s *Service) DownloadPdf(ctx context.Context, id session.SessionID, artifactID string) (PDFArtifact, io.ReadCloser, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return PDFArtifact{}, nil, err
	}
	if s.cfg.PDFArtifacts == nil {
		return PDFArtifact{}, nil, ErrPDFArtifactsUnavailable
	}
	meta, reader, err := s.cfg.PDFArtifacts.Open(ctx, id, artifactID)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		if ctx.Err() != nil {
			return PDFArtifact{}, nil, ctx.Err()
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidArgument) {
			return PDFArtifact{}, nil, err
		}
		return PDFArtifact{}, nil, ErrInternal
	}
	if reader == nil || meta.ID != artifactID || meta.Size <= 0 || meta.Size > maxPDFUploadBytes {
		if reader != nil {
			_ = reader.Close()
		}
		return PDFArtifact{}, nil, ErrInternal
	}
	return meta, reader, nil
}
