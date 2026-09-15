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
	reader  tool.BoundedWorkspaceReader
	path    string
	size    int64
	version string
}

func (b workspaceReviewEvidenceBackend) Size(context.Context) (int64, error) { return b.size, nil }

func (b workspaceReviewEvidenceBackend) ReadAt(ctx context.Context, offset, size int64) (string, error) {
	if offset != 0 || size != b.size {
		return "", fmt.Errorf("%w: unsupported evidence range", errEvidenceDenied)
	}
	content, version, err := b.reader.ReadVersionBounded(ctx, b.path, b.size)
	if err != nil {
		return "", err
	}
	encoded, err := tool.EncodeFileVersion(version)
	if err != nil || encoded != b.version {
		return "", fmt.Errorf("%w: evidence changed after inventory", errEvidenceDenied)
	}
	return session.ToValidUTF8(string(content)), nil
}

type memoryReviewEvidenceBackend struct{ content string }

func (b memoryReviewEvidenceBackend) Size(context.Context) (int64, error) {
	return int64(len(b.content)), nil
}
func (b memoryReviewEvidenceBackend) ReadAt(_ context.Context, offset, size int64) (string, error) {
	if offset != 0 || size != int64(len(b.content)) {
		return "", fmt.Errorf("%w: unsupported evidence range", errEvidenceDenied)
	}
	return b.content, nil
}

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
		content := prep.Result.Content
		if len(prep.Result.Parts) > 0 {
			if encoded, err := json.Marshal(prep.Result.Parts); err == nil {
				content += "\n" + string(encoded)
			}
		}
		content = session.ToValidUTF8(content)
		if int64(len(content)) <= maxReviewEvidenceRead {
			candidates = append(candidates, reviewEvidenceCandidate{Kind: "tool_result", Display: req.EffectiveCall.Name + " result", Version: "result-v1", Complete: true, Authorized: true, Binding: binding, Backend: memoryReviewEvidenceBackend{content: content}})
		}
	}

	paths := reviewEvidencePaths(req.EffectiveCall)
	if len(paths) > 0 {
		reader, ok := prep.Environment.Workspace().(tool.BoundedWorkspaceReader)
		if !ok {
			complete = false
		} else {
			seen := make(map[string]struct{}, len(paths))
			for _, path := range paths {
				if _, duplicate := seen[path]; duplicate {
					continue
				}
				seen[path] = struct{}{}
				info, err := prep.Environment.Workspace().Stat(ctx, path)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil || info.IsDir || info.Size > maxReviewEvidenceRead {
					complete = false
					continue
				}
				content, version, err := reader.ReadVersionBounded(ctx, path, maxReviewEvidenceRead)
				if err != nil {
					complete = false
					continue
				}
				encoded, err := tool.EncodeFileVersion(version)
				if err != nil {
					complete = false
					continue
				}
				candidates = append(candidates, reviewEvidenceCandidate{Kind: "text_file", Display: path, Version: encoded, Complete: true, Authorized: true, Binding: binding, Backend: workspaceReviewEvidenceBackend{reader: reader, path: path, size: int64(len(content)), version: encoded}})
			}
		}
	}

	source, evidence, inventoryComplete := newFiniteReviewEvidenceSource(ctx, binding, candidates, req.Capacity)
	var closeOnce sync.Once
	closeSource := func() { closeOnce.Do(source.CloseReviewEvidence) }
	return agent.PreparedReviewEvidence{Source: source, Evidence: evidence, Complete: complete && inventoryComplete, Close: closeSource}, nil
}

func reviewEvidencePaths(call session.ToolCall) []string {
	var args map[string]json.RawMessage
	if json.Unmarshal(call.Args, &args) != nil {
		return nil
	}
	var paths []string
	for _, key := range []string{"path", "source", "destination"} {
		var value string
		if json.Unmarshal(args[key], &value) == nil && value != "" {
			paths = append(paths, value)
		}
	}
	if call.Name != tool.ShellToolName {
		return paths
	}
	var command string
	if json.Unmarshal(args["command"], &command) != nil {
		return paths
	}
	if script, ok := exactShellScriptPath(command); ok {
		paths = append(paths, script)
	}
	return paths
}

func exactShellScriptPath(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || strings.ContainsAny(trimmed, ";&|$`()<>\\\n\r") {
		return "", false
	}
	fields := strings.Fields(trimmed)
	if strings.Join(fields, " ") != trimmed {
		return "", false
	}
	if len(fields) == 1 && (strings.HasPrefix(fields[0], "./") || strings.HasSuffix(fields[0], ".sh")) {
		return fields[0], true
	}
	if len(fields) == 2 && (fields[0] == "sh" || fields[0] == "bash" || fields[0] == "dash") && (strings.HasPrefix(fields[1], "./") || strings.HasSuffix(fields[1], ".sh")) {
		return fields[1], true
	}
	return "", false
}
