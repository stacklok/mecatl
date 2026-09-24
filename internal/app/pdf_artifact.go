package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"iter"
	"slices"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

var errPDFRequestUnavailable = errors.New("PDF artifact is unavailable for the selected session")

// pdfReferenceProvider resolves only the model request copy. Conversation
// history and persisted snapshots retain reference-only PDF parts.
type pdfReferenceProvider struct {
	inner     port.LLMProvider
	artifacts server.PDFArtifactLifecycle
}

func (p pdfReferenceProvider) Capabilities() port.ProviderCapabilities {
	caps := p.inner.Capabilities()
	caps.PDF = caps.PDF && p.artifacts != nil
	return caps
}

func (p pdfReferenceProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var copied bool
	clonedParts := make(map[int]bool)
	for i, message := range req.Messages {
		for j, part := range message.Parts {
			if part.Kind != session.MediaPDF {
				continue
			}
			if p.artifacts == nil || !p.Capabilities().PDF || len(part.Data) != 0 || part.ArtifactID == "" {
				return nil, errPDFRequestUnavailable
			}
			id, ok := port.SessionIDFromContext(ctx)
			if !ok || id == "" {
				return nil, errPDFRequestUnavailable
			}
			if !copied {
				req.Messages = slices.Clone(req.Messages)
				copied = true
			}
			if !clonedParts[i] {
				req.Messages[i].Parts = slices.Clone(message.Parts)
				clonedParts[i] = true
			}
			meta, reader, err := p.artifacts.Open(ctx, id, part.ArtifactID)
			if err != nil {
				return nil, errPDFRequestUnavailable
			}
			data, readErr := io.ReadAll(io.LimitReader(reader, pdfartifact.MaxPDFBytes+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > pdfartifact.MaxPDFBytes || int64(len(data)) != meta.Size || part.Name != meta.Name || part.Size != meta.Size || part.SHA256 != meta.SHA256 {
				return nil, errPDFRequestUnavailable
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != meta.SHA256 {
				return nil, errPDFRequestUnavailable
			}
			part.Data = data
			req.Messages[i].Parts[j] = part
		}
	}
	return p.inner.Stream(ctx, req)
}

// pdfPromptCommitStore decorates the engine's guarded store. It only needs the
// base SessionStore interface because engine persistence calls Save/Load.
type pdfPromptCommitStore struct {
	port.SessionStore
	artifacts server.PDFArtifactLifecycle
}

func (s pdfPromptCommitStore) Save(ctx context.Context, sess *session.Session) error {
	return saveAndCommitPDFPrompt(ctx, s.SessionStore, sess, s.artifacts)
}

// pdfPromptRedisStore keeps Redis's optional interfaces visible to Service
// while adding the same post-save marker update to service-owned writes.
type pdfPromptRedisStore struct {
	*redisstore.Store
	artifacts server.PDFArtifactLifecycle
}

func (s pdfPromptRedisStore) Save(ctx context.Context, sess *session.Session) error {
	return saveAndCommitPDFPrompt(ctx, s.Store, sess, s.artifacts)
}

func saveAndCommitPDFPrompt(ctx context.Context, store port.SessionStore, sess *session.Session, artifacts server.PDFArtifactLifecycle) error {
	current := pdfPromptRefs(sess)
	var previous map[string]struct{}
	if len(current) != 0 {
		if prior, err := store.Load(ctx, sess.ID); err == nil {
			previous = pdfPromptRefs(prior)
		}
	}
	if err := store.Save(ctx, sess); err != nil {
		return err
	}
	if artifacts == nil || len(current) == 0 {
		return nil
	}
	var added []string
	for id := range current {
		if _, existed := previous[id]; !existed {
			added = append(added, id)
		}
	}
	if len(added) != 0 {
		// A successful authoritative snapshot is the source of truth. If the
		// marker update fails, reconciliation finds the reference and repairs it.
		_ = artifacts.CommitPrompt(ctx, sess.ID, added)
	}
	return nil
}

func pdfPromptRefs(sess *session.Session) map[string]struct{} {
	if sess == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, message := range sess.Conversation.Messages {
		if message.Role != session.RoleUser {
			continue
		}
		for _, part := range message.Parts {
			if part.Kind == session.MediaPDF && part.ArtifactID != "" {
				seen[part.ArtifactID] = struct{}{}
			}
		}
	}
	return seen
}

func pdfServiceStore(store port.SessionStore, artifacts server.PDFArtifactLifecycle) port.SessionStore {
	if artifacts == nil {
		return store
	}
	redis, ok := store.(*redisstore.Store)
	if !ok {
		return store
	}
	return pdfPromptRedisStore{Store: redis, artifacts: artifacts}
}

func configuredPDFModelCapability(cfg Config, reg *providerRegistry, providerID, modelID string) port.ProviderCapabilities {
	caps := modelCapability(reg, providerID, modelID)
	if cfg.pdfArtifacts == nil || providerID != providerOpenAI && providerID != providerAnthropic {
		caps.PDF = false
	}
	return caps
}

func buildPDFArtifactStore(ctx context.Context, cfg Config, store port.SessionStore, lease port.SessionLease) (*pdfartifact.Store, error) {
	if cfg.PDFArtifactS3.Bucket == "" {
		if cfg.PDFArtifactS3.Region != "" || cfg.PDFArtifactS3.Endpoint != "" {
			return nil, errors.New("PDF artifact S3 bucket is required")
		}
		return nil, nil
	}
	metadata, ok := store.(*redisstore.Store)
	if !ok || lease == nil {
		return nil, errors.New("PDF artifacts require Redis metadata and a session lease")
	}
	objects, err := pdfartifact.NewS3(ctx, cfg.PDFArtifactS3)
	if err != nil {
		return nil, errors.New("PDF artifact S3 configuration is unavailable")
	}
	return pdfartifact.New(metadata, objects), nil
}

func startPDFArtifactReconcile(parent context.Context, store *pdfartifact.Store, svc *server.Service, lease port.SessionLease, leaseOwner string, diag port.Diagnostics) func() {
	if store == nil {
		return func() {}
	}
	store.LeaseActive = func(ctx context.Context, id session.SessionID) (bool, error) {
		if svc.IsLive(id) {
			return true, nil
		}
		trial, err := lease.Acquire(ctx, id, leaseOwner+":pdf-reconcile")
		if errors.Is(err, port.ErrLeaseHeld) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if err := lease.Release(ctx, trial); err != nil {
			return false, err
		}
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			if err := store.Reconcile(ctx); err != nil && ctx.Err() == nil {
				diag.Log(ctx, port.LevelWarn, "PDF artifact reconciliation unavailable")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
