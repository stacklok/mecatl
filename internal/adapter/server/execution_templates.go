package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var errExecutionUnauthenticated = errors.New("verified execution template owner required")

// ErrExecutionTemplatesDisabled reports an unwired per-owner template policy or catalog.
var ErrExecutionTemplatesDisabled = errors.New("execution templates disabled")

// Public execution capability names.
const (
	ExecutionCapabilityFilesystem        = "filesystem"
	ExecutionCapabilityForegroundCommand = "foreground-command"
)

// ErrExecutionCapabilityRefused reports a well-formed capability request this
// deployment, placement, or session kind cannot honor. Requests are refused,
// never narrowed.
var ErrExecutionCapabilityRefused = fmt.Errorf("%w: execution capability request refused",
	ErrFailedPrecondition)

// ExecutionCapabilityRefusal is a provider's capability refusal; Reason is
// returned to the client.
type ExecutionCapabilityRefusal struct{ Reason string }

func (e *ExecutionCapabilityRefusal) Error() string {
	return ErrExecutionCapabilityRefused.Error() + ": " + e.Reason
}

func (*ExecutionCapabilityRefusal) Unwrap() error { return ErrExecutionCapabilityRefused }

func refuseCapabilities(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrExecutionCapabilityRefused, fmt.Sprintf(format, args...))
}

// ExecutionCapabilityEnforcer is implemented by placement providers whose Bind
// admits PlacementBindRequest.Capabilities and reports the admitted set.
type ExecutionCapabilityEnforcer interface {
	EnforcesExecutionCapabilities(PlacementSelector) bool
}

type executionCapabilitiesKey struct{}

// ContextWithExecutionCapabilities carries the capabilities a session engine
// factory may register tools for.
func ContextWithExecutionCapabilities(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, executionCapabilitiesKey{}, slices.Clone(names))
}

// ExecutionCapabilitiesFromContext reports the set attached by the service;
// ok is false for placements that do not enforce capabilities.
func ExecutionCapabilitiesFromContext(ctx context.Context) (names []string, ok bool) {
	names, ok = ctx.Value(executionCapabilitiesKey{}).([]string)
	return slices.Clone(names), ok
}

// ExecutionTemplateInfo is bounded inert display metadata, never an execution recipe.
type ExecutionTemplateInfo struct {
	ID, Revision, Name, Description, DisplayToken string
	Extensions                                    map[string]string
	// Capabilities is the template's declared set, not a session's effective set.
	Capabilities []string
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

// stampExecutionCapabilities records admitted ∩ registered; nil admitted
// means the placement does not enforce capabilities.
func stampExecutionCapabilities(
	sess *session.Session,
	engine *agent.Engine,
	env tool.Environment,
	admitted []string,
) {
	facts := &session.ExecutionCapabilities{}
	if engine != nil {
		facts.Files = engine.HasTool("Read") && env.Workspace() != nil && env.Workspace().Root() != ""
		facts.Shell = engine.HasTool(tool.ShellToolName) && env.CommandRunner() != nil
	}
	if admitted != nil {
		facts.Files = facts.Files && slices.Contains(admitted, ExecutionCapabilityFilesystem)
		facts.Shell = facts.Shell && slices.Contains(admitted, ExecutionCapabilityForegroundCommand)
	}
	sess.ExecutionCapabilities = facts
}

func recordedExecutionCapabilities(sess *session.Session) []string {
	if sess == nil || sess.ExecutionCapabilities == nil {
		return nil
	}
	out := []string{}
	if sess.ExecutionCapabilities.Files {
		out = append(out, ExecutionCapabilityFilesystem)
	}
	if sess.ExecutionCapabilities.Shell {
		out = append(out, ExecutionCapabilityForegroundCommand)
	}
	return out
}

func capabilityFacts(names []string) *session.ExecutionCapabilities {
	return &session.ExecutionCapabilities{
		Files: slices.Contains(names, ExecutionCapabilityFilesystem),
		Shell: slices.Contains(names, ExecutionCapabilityForegroundCommand),
	}
}

// enforcedCapabilities is sess's recorded set when its placement enforces
// capabilities, and nil otherwise.
func (s *Service) enforcedCapabilities(sess *session.Session) []string {
	if s.cfg.ExecutionAccess == nil || !s.cfg.ExecutionAccess.Applies(sess.EnvironmentRef) {
		return nil
	}
	return recordedExecutionCapabilities(sess)
}

// fireCapabilities is what a scheduled fire of source requests. The fire
// mints its definition's authority afresh, so an agent-bound source passes on
// only what that ceiling left it.
func (s *Service) fireCapabilities(source *session.Session) []string {
	requested := s.enforcedCapabilities(source)
	if requested == nil || source.AgentDefinitionName == "" {
		return requested
	}
	return executionSessionCapabilities(source)
}

func withExecutionCapabilities(ctx context.Context, names []string) context.Context {
	if names == nil {
		return ctx
	}
	return ContextWithExecutionCapabilities(ctx, names)
}

// sharedEngineExceeds reports whether the shared engine registers tools for a
// capability outside names.
func (s *Service) sharedEngineExceeds(names []string) bool {
	if names == nil || s.cfg.Engine == nil {
		return false
	}
	files := slices.Contains(names, ExecutionCapabilityFilesystem)
	shell := slices.Contains(names, ExecutionCapabilityForegroundCommand)
	return !files && s.cfg.Engine.HasTool("Read") ||
		!shell && s.cfg.Engine.HasTool(tool.ShellToolName)
}

// withoutWithheldRunner drops the command runner from a run environment whose
// session does not have foreground-command.
func (s *Service) withoutWithheldRunner(
	sess *session.Session,
	env tool.Environment,
) (tool.Environment, error) {
	names := s.enforcedCapabilities(sess)
	if names == nil || slices.Contains(names, ExecutionCapabilityForegroundCommand) ||
		env.CommandRunner() == nil {
		return env, nil
	}
	return tool.NewEnvironment(env.Ref(), env.Workspace(), env.ReadLedger(), nil)
}

// requestedCapabilities validates the request's names and returns them sorted.
func (e ExecutionSelection) requestedCapabilities() ([]string, error) {
	for i, name := range e.Capabilities {
		known := name == ExecutionCapabilityFilesystem ||
			name == ExecutionCapabilityForegroundCommand
		if !known || slices.Contains(e.Capabilities[:i], name) {
			return nil, fmt.Errorf(
				"%w: execution capabilities must list %q or %q at most once each",
				ErrInvalidArgument,
				ExecutionCapabilityFilesystem,
				ExecutionCapabilityForegroundCommand,
			)
		}
	}
	return slices.Sorted(slices.Values(e.Capabilities)), nil
}

func (s *Service) refuseCapabilityRequest(
	requested []string,
	selector PlacementSelector,
	opts createSessionOpts,
) error {
	if len(requested) == 0 {
		return nil
	}
	enforcer, ok := s.cfg.PlacementProvider.(ExecutionCapabilityEnforcer)
	switch {
	case opts.debugTargetID != "":
		return refuseCapabilities("debug sessions cannot request execution capabilities")
	case len(requested) != 2:
		return refuseCapabilities("sessions must have both %q and %q; "+
			"file-only sessions are not supported yet",
			ExecutionCapabilityFilesystem, ExecutionCapabilityForegroundCommand)
	case s.cfg.ExecutionShellDisabled:
		return refuseCapabilities("%q is disabled on this deployment",
			ExecutionCapabilityForegroundCommand)
	case opts.placement != nil || !ok || !enforcer.EnforcesExecutionCapabilities(selector):
		return refuseCapabilities("the selected placement cannot enforce execution capabilities")
	}
	return nil
}

// refuseDisabledCapabilities refuses a successor's request for what this
// deployment disables, such as a Shell source after --no-shell was set.
func (s *Service) refuseDisabledCapabilities(requested []string) error {
	if !s.cfg.ExecutionShellDisabled ||
		!slices.Contains(requested, ExecutionCapabilityForegroundCommand) {
		return nil
	}
	return refuseCapabilities("%q is disabled on this deployment",
		ExecutionCapabilityForegroundCommand)
}

// checkAdmittedCapabilities refuses a binding that does not honor requested
// exactly. A nil admitted set never honors a non-empty request.
func checkAdmittedCapabilities(requested, admitted []string) error {
	for _, name := range admitted {
		if name != ExecutionCapabilityFilesystem && name != ExecutionCapabilityForegroundCommand {
			return ErrInvalidPlacementBinding
		}
	}
	if len(requested) == 0 {
		return nil
	}
	if admitted == nil {
		return refuseCapabilities("the selected placement cannot enforce execution capabilities")
	}
	for _, name := range requested {
		if !slices.Contains(admitted, name) {
			return refuseCapabilities("the placement did not admit %q", name)
		}
	}
	return nil
}

// checkEffectiveCapabilities refuses a session whose tools or bound authority
// cannot provide a requested capability, such as an agent definition ceiling.
func checkEffectiveCapabilities(requested []string, sess *session.Session) error {
	effective := executionSessionCapabilities(sess)
	for _, name := range requested {
		if slices.Contains(effective, name) {
			continue
		}
		if sess.AgentDefinitionName != "" {
			return refuseCapabilities("agent definition %q does not allow %q",
				sess.AgentDefinitionName, name)
		}
		return refuseCapabilities("this session's tools do not provide %q", name)
	}
	return nil
}

// checkRecordedCapabilities refuses a successor whose recorded set lost one
// its source recorded; bound authority is not part of either set.
func checkRecordedCapabilities(requested []string, sess *session.Session) error {
	recorded := recordedExecutionCapabilities(sess)
	for _, name := range requested {
		if !slices.Contains(recorded, name) {
			return refuseCapabilities("this session's tools do not provide %q", name)
		}
	}
	return nil
}

// executionSessionCapabilities is the effective list: admitted by the
// environment, registered as tools, and allowed by bound authority.
func executionSessionCapabilities(sess *session.Session) []string {
	out := []string{}
	if sess == nil || sess.ExecutionCapabilities == nil || sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return out
	}
	files, shell := sess.ExecutionCapabilities.Files, sess.ExecutionCapabilities.Shell
	if authority, bound := sess.BoundAuthority(); bound {
		files = files && authority.CapabilitySet.FileSystem && authority.CapabilitySet.AllowsTool("Read")
		shell = shell && authority.CapabilitySet.AllowsTool(tool.ShellToolName)
	}
	if files {
		out = append(out, ExecutionCapabilityFilesystem)
	}
	if shell {
		out = append(out, ExecutionCapabilityForegroundCommand)
	}
	return out
}

func validCatalogCapabilities(names []string) bool {
	if len(names) > 16 {
		return false
	}
	for _, name := range names {
		const allowed = "abcdefghijklmnopqrstuvwxyz0123456789-"
		if name == "" || len(name) > 64 || strings.Trim(name, allowed) != "" {
			return false
		}
	}
	return true
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
		if !validCatalogCapabilities(item.Capabilities) {
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
	// Capabilities is the template request; empty means the template's set.
	Capabilities []string
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
