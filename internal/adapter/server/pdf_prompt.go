package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/stacklok/mecatl/engine/session"
)

const pdfMIMEType = "application/pdf"

func hasPDFPromptPart(parts []session.Content) bool {
	for _, part := range parts {
		if part.Kind == session.MediaPDF {
			return true
		}
	}
	return false
}

// resolvePDFPromptParts authorizes each reference within the already-owned
// session and fills metadata from storage before the prompt can be recorded.
func (s *Service) resolvePDFPromptParts(ctx context.Context, sess *session.Session, parts []session.Content) ([]session.Content, error) {
	if !hasPDFPromptPart(parts) {
		return parts, nil
	}
	if s.cfg.Artifacts == nil {
		return nil, ErrArtifactsUnavailable
	}
	if !s.sessionCapabilitiesFor(sess).PDF {
		return nil, fmt.Errorf("%w: selected model does not accept PDF input", ErrInvalidArgument)
	}
	out := slices.Clone(parts)
	for i, part := range out {
		if part.Kind != session.MediaPDF {
			continue
		}
		if part.BlockKind != "" || part.MIMEType != pdfMIMEType || len(part.Data) != 0 || part.URL != "" || !validArtifactID(part.ArtifactID) {
			return nil, fmt.Errorf("%w: invalid PDF artifact reference", ErrInvalidArgument)
		}
		meta, err := s.cfg.Artifacts.Resolve(ctx, sess.ID, part.ArtifactID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, ErrInternal
		}
		if meta.MIMEType != pdfMIMEType {
			return nil, ErrInternal
		}
		resolved, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
		if err != nil {
			return nil, ErrInternal
		}
		out[i] = resolved
	}
	if err := session.ValidateMediaParts(out); err != nil {
		return nil, fmt.Errorf("%w: invalid PDF prompt size", ErrInvalidArgument)
	}
	return out, nil
}
