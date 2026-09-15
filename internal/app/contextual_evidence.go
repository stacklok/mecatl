package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type workspaceReviewEvidenceBackend struct {
	reader    tool.BoundedWorkspaceRangeReader
	path      string
	size      int64
	version   string
	authorize func(context.Context) error
}

func (b workspaceReviewEvidenceBackend) Size(context.Context) (int64, error) { return b.size, nil }
func (b workspaceReviewEvidenceBackend) EvidencePageRanges(ctx context.Context, offset, total int64) ([]reviewEvidencePage, error) {
	return lineBoundedEvidencePageRanges(ctx, b, offset, total)
}

func (b workspaceReviewEvidenceBackend) ReadAt(ctx context.Context, offset, size int64) (string, error) {
	if b.authorize == nil || b.authorize(ctx) != nil {
		return "", fmt.Errorf("%w: evidence read permission is unavailable", errEvidenceDenied)
	}
	content, version, total, err := b.reader.ReadVersionRangeBounded(ctx, b.path, offset, size, maxReviewEvidenceBytes)
	if err != nil {
		return "", err
	}
	encoded, err := tool.EncodeFileVersion(version)
	if err != nil || encoded != b.version || total != b.size || int64(len(content)) != size {
		return "", fmt.Errorf("%w: evidence changed after inventory", errEvidenceDenied)
	}
	return session.ToValidUTF8(string(content)), nil
}

type memoryReviewEvidenceBackend struct{ content string }

func (b memoryReviewEvidenceBackend) Size(context.Context) (int64, error) {
	return int64(len(b.content)), nil
}
func (b memoryReviewEvidenceBackend) EvidencePageRanges(ctx context.Context, offset, total int64) ([]reviewEvidencePage, error) {
	return lineBoundedEvidencePageRanges(ctx, b, offset, total)
}
func (b memoryReviewEvidenceBackend) ReadAt(_ context.Context, offset, size int64) (string, error) {
	if offset < 0 || size < 0 || offset+size > int64(len(b.content)) {
		return "", fmt.Errorf("%w: unsupported evidence range", errEvidenceDenied)
	}
	return b.content[offset : offset+size], nil
}

//nolint:gocyclo // Preparation keeps binding, authorization-before-I/O, capacity, and cleanup in one auditable path.
func (r *guardrailActionReviewer) PrepareReviewEvidence(ctx context.Context, prep agent.ReviewEvidencePreparation) (agent.PreparedReviewEvidence, error) {
	req := prep.Request
	if prep.Environment.Ref() != req.Environment || req.Event.SessionID == "" || req.ReviewID == "" {
		return agent.PreparedReviewEvidence{}, fmt.Errorf("%w: invalid preparation binding", errEvidenceDenied)
	}
	owner := "ownerless:" + req.Event.SessionID
	if prep.Owner != nil {
		owner = fmt.Sprintf("%x", session.PrincipalScopeHash(prep.Owner))
	}
	binding := reviewEvidenceBinding{
		ReviewID: req.ReviewID, Owner: owner, SessionID: session.SessionID(req.Event.SessionID),
		Environment: req.Environment, Caller: req.Caller, CheckerProviderID: r.providerID,
		CheckerModelID: r.modelID, ExpiresAt: time.Now().Add(reviewTotalDeadline),
	}
	candidates := make([]reviewEvidenceCandidate, 0, 4)
	complete := true

	if prep.Result != nil {
		content, resultComplete := reviewableResultContent(*prep.Result)
		complete = complete && resultComplete
		if int64(len(content)) <= maxReviewEvidenceBytes {
			candidates = append(candidates, reviewEvidenceCandidate{Kind: "tool_result", Display: req.EffectiveCall.Name + " result", Version: "result-v1", Complete: resultComplete, Authorized: true, Binding: binding, Backend: memoryReviewEvidenceBackend{content: content}})
		} else {
			complete = false
		}
	}

	paths := reviewEvidencePaths(req.EffectiveCall)
	if len(paths) > 0 {
		reader, ok := prep.Environment.Workspace().(tool.BoundedWorkspaceRangeReader)
		if !ok {
			complete = false
		} else {
			seen := make(map[string]struct{}, len(paths))
			for _, path := range paths {
				if _, duplicate := seen[path]; duplicate {
					continue
				}
				seen[path] = struct{}{}
				if prep.Authorize == nil {
					complete = false
					continue
				}
				readArgs, err := json.Marshal(struct {
					Path string `json:"path"`
				}{Path: path})
				readCall := session.NewToolCall(req.EffectiveCall.ID, readToolName, readArgs)
				authorize := func(authCtx context.Context) error { return prep.Authorize(authCtx, readCall) }
				if err != nil || authorize(ctx) != nil {
					complete = false
					continue
				}
				content, version, total, err := reader.ReadVersionRangeBounded(ctx, path, 0, maxReviewEvidenceRead, maxReviewEvidenceBytes)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					complete = false
					continue
				}
				encoded, err := tool.EncodeFileVersion(version)
				if err != nil {
					complete = false
					continue
				}
				_ = content // The bounded first page establishes version and total size.
				candidates = append(candidates, reviewEvidenceCandidate{Kind: "text_file", Display: path, Version: encoded, Complete: true, Authorized: true, Binding: binding, Backend: workspaceReviewEvidenceBackend{reader: reader, path: path, size: total, version: encoded, authorize: authorize}})
			}
		}
	}

	source, evidence, inventoryComplete := newFiniteReviewEvidenceSource(ctx, binding, candidates, req.Capacity)
	var closeOnce sync.Once
	closeSource := func() { closeOnce.Do(source.CloseReviewEvidence) }
	return agent.PreparedReviewEvidence{Source: source, Evidence: evidence, Complete: complete && inventoryComplete, Close: closeSource}, nil
}

func reviewEvidencePaths(call session.ToolCall) []string {
	return tool.LocalFileOperands(call.Name, call.Args)
}

func exactShellScriptPath(command string) (string, bool) {
	args, _ := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	paths := tool.LocalFileOperands(tool.ShellToolName, args)
	if len(paths) != 1 {
		return "", false
	}
	return paths[0], true
}

func reviewableResultContent(result session.ToolResult) (string, bool) {
	var b strings.Builder
	b.WriteString(result.Content)
	complete := true
	for _, part := range result.Parts {
		if part.Kind == session.MediaImage || part.Kind == session.MediaAudio || part.BlockKind == session.BlockImage || part.BlockKind == session.BlockAudio || len(part.Data) > 0 {
			complete = false
			continue
		}
		if text := session.ToolBlockText(part); text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
	}
	return session.ToValidUTF8(b.String()), complete
}
