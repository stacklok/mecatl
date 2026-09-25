package server

import (
	"context"
	"errors"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// UploadArtifact stages a private PDF only after the caller owns the exact session.
func (s *Service) UploadArtifact(ctx context.Context, id session.SessionID, name, mimeType string, source io.Reader) (Artifact, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return Artifact{}, err
	}
	if s.cfg.Artifacts == nil {
		return Artifact{}, ErrArtifactsUnavailable
	}
	if mimeType != "application/pdf" {
		return Artifact{}, ErrInvalidArgument
	}
	artifact, err := s.cfg.Artifacts.Stage(ctx, id, name, mimeType, source)
	if err == nil {
		return artifact, nil
	}
	if ctx.Err() != nil {
		return Artifact{}, ctx.Err()
	}
	if errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrNotFound) {
		return Artifact{}, err
	}
	return Artifact{}, ErrInternal
}

// DownloadArtifact authorizes the exact persisted session before opening any PDF
// object. A caller owns the returned reader and must close it.
func (s *Service) DownloadArtifact(ctx context.Context, id session.SessionID, artifactID string) (Artifact, io.ReadCloser, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return Artifact{}, nil, err
	}
	if s.cfg.Artifacts == nil {
		return Artifact{}, nil, ErrArtifactsUnavailable
	}
	meta, reader, err := s.cfg.Artifacts.Open(ctx, id, artifactID)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		if ctx.Err() != nil {
			return Artifact{}, nil, ctx.Err()
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidArgument) {
			return Artifact{}, nil, err
		}
		return Artifact{}, nil, ErrInternal
	}
	if reader == nil || meta.ID != artifactID || meta.MIMEType != "application/pdf" || meta.Size <= 0 || meta.Size > maxPDFUploadBytes {
		if reader != nil {
			_ = reader.Close()
		}
		return Artifact{}, nil, ErrInternal
	}
	return meta, reader, nil
}
