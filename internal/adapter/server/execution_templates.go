package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var errExecutionUnauthenticated = errors.New("verified execution template owner required")

// ErrExecutionTemplatesDisabled reports an unwired per-owner template policy or catalog.
var ErrExecutionTemplatesDisabled = errors.New("execution templates disabled")

// ExecutionTemplateInfo is bounded inert display metadata, never an execution recipe.
type ExecutionTemplateInfo struct {
	ID, Revision, Name, Description, DisplayToken string
	Extensions                                    map[string]string
	DeclaredExecutionFiles, DeclaredBuiltInShell  bool
}

// ExecutionTemplateCatalog is optional and must enumerate only operator-authorized
// revisions for this deployment. The host requires a verified owner before use;
// Bind reauthorizes the exact revision for that owner.
type ExecutionTemplateCatalog interface {
	ListExecutionTemplates(context.Context, *session.Principal) ([]ExecutionTemplateInfo, string, error)
}

func (s *Service) executionCatalogAvailable() bool {
	_, ok := s.cfg.PlacementProvider.(ExecutionTemplateCatalog)
	return ok && s.cfg.ExecutionTemplateAllowed != nil
}

func stampExecutionCapabilities(sess *session.Session, engine *agent.Engine, env tool.Environment) {
	facts := &session.ExecutionCapabilities{}
	if engine != nil {
		facts.Files = engine.HasTool("Read") && env.Workspace() != nil && env.Workspace().Root() != ""
		facts.Shell = engine.HasTool(tool.ShellToolName) && env.CommandRunner() != nil
	}
	sess.ExecutionCapabilities = facts
}

func executionSessionCapabilities(sess *session.Session) (bool, bool) {
	if sess == nil || sess.ExecutionCapabilities == nil || sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return false, false
	}
	files, shell := sess.ExecutionCapabilities.Files, sess.ExecutionCapabilities.Shell
	if authority, bound := sess.BoundAuthority(); bound {
		files = files && authority.CapabilitySet.FileSystem && authority.CapabilitySet.AllowsTool("Read")
		shell = shell && authority.CapabilitySet.AllowsTool(tool.ShellToolName)
	}
	return files, shell
}

// ListExecutionTemplates returns a filtered, bounded inventory for the verified owner.
//
//nolint:gocyclo // The public catalog boundary validates every untrusted metadata field.
func (s *Service) ListExecutionTemplates(ctx context.Context) ([]ExecutionTemplateInfo, string, error) {
	if !s.cfg.OwnershipEnforced || session.PrincipalFromContext(ctx) == nil {
		return nil, "", errExecutionUnauthenticated
	}
	if !s.executionCatalogAvailable() {
		return nil, "", ErrExecutionTemplatesDisabled
	}
	catalog, ok := s.cfg.PlacementProvider.(ExecutionTemplateCatalog)
	if !ok {
		return nil, "", ErrPlacementUnavailable
	}
	items, _, err := catalog.ListExecutionTemplates(ctx, session.PrincipalFromContext(ctx))
	if err != nil {
		return nil, "", sanitizePlacementProviderError(err)
	}
	if len(items) > 64 {
		return nil, "", ErrInvalidPlacementBinding
	}
	filtered := make([]ExecutionTemplateInfo, 0, len(items))
	principal := session.PrincipalFromContext(ctx)
	for _, item := range items {
		if !s.cfg.ExecutionTemplateAllowed(principal, item.ID, item.Revision) {
			continue
		}
		if !SelectTemplate(item.ID, item.Revision).Valid() || len(item.Name) > 480 || utf8.RuneCountInString(item.Name) > 120 || len(item.Description) > 1024 || len(item.Extensions) > 128 || !utf8.ValidString(item.Name) || !utf8.ValidString(item.Description) || !SelectTemplate("display", item.DisplayToken).Valid() {
			return nil, "", ErrInvalidPlacementBinding
		}
		for key, value := range item.Extensions {
			if len(key) > 253 || len(value) > 256 || !utf8.ValidString(key) || !utf8.ValidString(value) {
				return nil, "", ErrInvalidPlacementBinding
			}
		}
		filtered = append(filtered, item)
	}
	raw, err := json.Marshal(filtered)
	if err != nil {
		return nil, "", ErrInvalidPlacementBinding
	}
	digest := sha256.Sum256(raw)
	return filtered, "v1-" + hex.EncodeToString(digest[:]), nil
}

// ExecutionSelection is the public create choice. The zero value selects the
// deployment default; none never allocates an execution environment.
type ExecutionSelection struct {
	Kind         PlacementSelectorKind
	ID, Revision string
}

func (e ExecutionSelection) placement() (PlacementSelector, SessionProfile, error) {
	switch e.Kind {
	case "", PlacementSelectorDefault:
		if e.ID == "" && e.Revision == "" {
			return DefaultPlacement(), ProfileDefault, nil
		}
	case PlacementSelectorNoFS:
		if e.ID == "" && e.Revision == "" {
			return NoFSPlacement(), ProfileNoFS, nil
		}
	case PlacementSelectorTemplate:
		selector := SelectTemplate(e.ID, e.Revision)
		if selector.Valid() && strings.TrimSpace(e.ID) == e.ID && strings.TrimSpace(e.Revision) == e.Revision {
			return selector, ProfileDefault, nil
		}
	}
	return PlacementSelector{}, "", fmt.Errorf("%w: invalid execution selection", ErrInvalidArgument)
}

// WithExecutionSelection carries a validated public selection to atomic Bind.
func WithExecutionSelection(e ExecutionSelection) CreateSessionOption {
	return func(o *createSessionOpts) { o.execution = e }
}
