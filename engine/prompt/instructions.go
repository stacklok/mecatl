package prompt

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// InstructionManifest identifies one emitted message without retaining its body.
type InstructionManifest struct {
	Kind, Provenance, SourceID, Directory, File string
	Partial, Omitted, HasGuidance               bool
}

// InstructionKindTurn0 identifies injected turn-zero guidance.
const (
	InstructionKindTurn0           = "instruction"
	InstructionProvenanceProject   = "project"
	InstructionProvenanceSoul      = "soul"
	InstructionProvenanceMemory    = "memory"
	InstructionProvenanceRules     = "rules"
	InstructionProvenanceUserModel = "user_model"
	InstructionProvenanceCustom    = "custom"
	InstructionProvenanceUnknown   = "unknown"
)

// InstructionAssembler resolves ordered ephemeral messages from admitted sources.
type InstructionAssembler interface {
	Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []InstructionManifest, error)
	// TargetScoped reports whether later target directories can change this assembler's contribution.
	TargetScoped() bool
}

// AssembleWithManifest nil-safely delegates to the canonical assembler method.
func AssembleWithManifest(ctx context.Context, a InstructionAssembler, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []InstructionManifest, error) {
	if a == nil {
		return nil, nil, nil
	}
	return a.Assemble(ctx, directories, state, maxContentBytes)
}

// RootAssembler reads only the selected source and its trusted source-relative subtree.
type RootAssembler struct {
	Source       tool.WorkspaceReader
	SourceID     string
	SourcePrefix string
}

// TargetScoped reports whether the assembler depends on a selected workspace target.
func (a RootAssembler) TargetScoped() bool { return a.Source != nil }

// Assemble resolves applicable project guidance while retaining the shared discovery snapshot.
func (a RootAssembler) Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []InstructionManifest, error) {
	if a.Source == nil {
		return nil, nil, nil
	}
	if a.SourceID == "" || !validInstructionDirectory(a.SourcePrefix) {
		return nil, nil, fmt.Errorf("invalid instruction source binding")
	}
	if maxContentBytes <= 0 {
		maxContentBytes = 65536
	}
	if state == nil {
		state = &session.InstructionSnapshot{}
	}
	if len(directories) == 0 {
		directories = []string{"."}
	}
	contentSkipped, err := a.discoverScopes(ctx, directories, state, maxContentBytes)
	if err != nil {
		return nil, nil, err
	}
	messages, rows := a.renderScopes(state)
	if contentSkipped {
		// Notices are request-local; never retain rejected paths in the snapshot.
		notice := "Project instructions: further scopes omitted (content budget exhausted)."
		if len(a.SourceID)+len("content")+1 > maxContentBytes-state.MetadataBytes() {
			notice = "Project instructions: further scopes omitted (instruction budget exhausted)."
		}
		messages = append(messages, session.NewUserMessage(notice))
		rows = append(rows, InstructionManifest{Kind: InstructionKindTurn0, Provenance: InstructionProvenanceProject, SourceID: a.SourceID})
	}
	if state.DiscoveryExhausted {
		messages = append(messages, session.NewUserMessage("Project instructions: further scopes omitted (metadata budget exhausted)."))
		rows = append(rows, InstructionManifest{Kind: InstructionKindTurn0, Provenance: InstructionProvenanceProject})
	}
	return messages, rows, nil
}

func (a RootAssembler) discoverScopes(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) (bool, error) {
	knownDirs := make(map[string]bool, len(state.Directories))
	for _, dir := range state.Directories {
		knownDirs[dir] = true
	}
	usedContent := 0
	knownScopes := make(map[string]bool, len(state.Scopes))
	for _, scope := range state.Scopes {
		if scope.SourceID == a.SourceID {
			knownScopes[scope.Directory] = true
		}
		usedContent += len(scope.Text)
	}
	contentSkipped := false
	for _, target := range directories {
		if !validInstructionDirectory(target) {
			continue
		}
		target = path.Clean(target)
		if !knownDirs[target] && !state.DiscoveryExhausted {
			if len(target)+1 > maxContentBytes-state.MetadataBytes() {
				state.DiscoveryExhausted = true
			} else {
				state.Directories = append(state.Directories, target)
				knownDirs[target] = true
			}
		}
		if state.DiscoveryExhausted {
			break
		}
		for _, dir := range instructionChain(path.Join(a.SourcePrefix, target)) {
			if knownScopes[dir] {
				continue
			}
			if usedContent >= maxContentBytes {
				contentSkipped = true
				break
			}
			reserve := len(a.SourceID) + len(dir) + len("CLAUDE.md") + 1
			if reserve > maxContentBytes-state.MetadataBytes() {
				state.DiscoveryExhausted = true
				break
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			scope, err := a.readScope(ctx, dir, maxContentBytes-usedContent)
			if err != nil {
				state.Scopes = append(state.Scopes, scope)
				return false, err
			}
			usedContent += len(scope.Text)
			state.Scopes = append(state.Scopes, scope)
			knownScopes[dir] = true
		}
		if state.DiscoveryExhausted {
			break
		}
	}
	return contentSkipped, nil
}

func (a RootAssembler) readScope(ctx context.Context, dir string, remaining int) (session.InstructionScope, error) {
	found, file, text, err := readInstructionScope(ctx, a.Source, dir)
	scope := session.InstructionScope{SourceID: a.SourceID, Directory: dir, File: file, Examined: true}
	if err != nil {
		scope.Unavailable = true
		return scope, err
	}
	if !found {
		return scope, nil
	}
	text = strings.ToValidUTF8(text, "")
	if len(text) > remaining {
		scope.Partial = true
		text = text[:remaining]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		if len(text) == 0 {
			scope.Omitted = true
		}
	}
	scope.Text = text
	return scope, nil
}

func (a RootAssembler) renderScopes(state *session.InstructionSnapshot) ([]session.Message, []InstructionManifest) {
	var messages []session.Message
	var rows []InstructionManifest
	for _, scope := range state.Scopes {
		if scope.SourceID != a.SourceID || (!scope.Unavailable && !scope.Omitted && scope.Text == "") {
			continue
		}
		execDir := scopeExecutionDirectory(a.SourcePrefix, scope.Directory)
		if !scopeApplies(state.Directories, execDir) {
			continue
		}
		if scope.Unavailable {
			messages = append(messages, session.NewUserMessage("Project instructions: selected guidance unavailable; ordinary tools remain available."))
			rows = append(rows, InstructionManifest{Kind: InstructionKindTurn0, Provenance: InstructionProvenanceProject, SourceID: a.SourceID})
			continue
		}
		marker := "Project instructions (" + path.Base(scope.File) + "): [scope: " + strconv.Quote(execDir) + "]"
		if scope.Partial {
			marker += " [TRUNCATED: remaining guidance omitted]"
		}
		if scope.Omitted {
			marker += " [OMITTED: content limit reached]"
		}
		messages = append(messages, session.NewUserMessage(marker+"\n\n"+scope.Text))
		rows = append(rows, InstructionManifest{Kind: InstructionKindTurn0, Provenance: InstructionProvenanceProject, SourceID: a.SourceID, Directory: execDir, File: scope.File, Partial: scope.Partial, Omitted: scope.Omitted, HasGuidance: scope.Text != ""})
	}
	return messages, rows
}

func scopeExecutionDirectory(prefix, directory string) string {
	if prefix == "." {
		return directory
	}
	if strings.HasPrefix(directory, prefix+"/") {
		return strings.TrimPrefix(directory, prefix+"/")
	}
	return "."
}

func scopeApplies(targets []string, directory string) bool {
	for _, target := range targets {
		if target == directory || directory == "." || strings.HasPrefix(target, directory+"/") {
			return true
		}
	}
	return false
}

func validInstructionDirectory(dir string) bool {
	return dir != "" && dir != ".." && !strings.HasPrefix(dir, "../") && !strings.HasPrefix(dir, "/") && !strings.Contains(dir, "\\") && path.Clean(dir) == dir
}

func instructionChain(target string) []string {
	chain := []string{"."}
	if target == "." {
		return chain
	}
	for _, part := range strings.Split(target, "/") {
		chain = append(chain, path.Join(chain[len(chain)-1], part))
	}
	return chain
}

func manifestFor(messages []session.Message, provenance string) []InstructionManifest {
	rows := make([]InstructionManifest, len(messages))
	for i := range rows {
		rows[i] = InstructionManifest{Kind: InstructionKindTurn0, Provenance: provenance, HasGuidance: true}
	}
	return rows
}

// MultiAssembler preserves contributor order while sharing the one snapshot budget.
type MultiAssembler struct{ Assemblers []InstructionAssembler }

// NewMultiAssembler combines assemblers while preserving contributor order.
func NewMultiAssembler(assemblers ...InstructionAssembler) MultiAssembler {
	return MultiAssembler{Assemblers: assemblers}
}

// TargetScoped reports whether any contributor depends on a selected target.
func (m MultiAssembler) TargetScoped() bool {
	for _, child := range m.Assemblers {
		if child != nil && child.TargetScoped() {
			return true
		}
	}
	return false
}

// Assemble combines each contributor's messages and manifests within one snapshot budget.
func (m MultiAssembler) Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []InstructionManifest, error) {
	if state == nil {
		state = &session.InstructionSnapshot{}
	}
	if maxContentBytes <= 0 {
		maxContentBytes = 65536
	}
	var messages []session.Message
	var rows []InstructionManifest
	for _, a := range m.Assemblers {
		child, manifest, err := AssembleWithManifest(ctx, a, directories, state, maxContentBytes)
		if err != nil {
			return messages, rows, err
		}
		messages = append(messages, child...)
		rows = append(rows, manifest...)
	}
	if state != nil {
		remaining := maxContentBytes - state.MetadataBytes()
		var kept []session.Message
		var keptRows []InstructionManifest
		var notices []session.Message
		var noticeRows []InstructionManifest
		seen := make(map[string]bool)
		global := false
		for i, row := range rows {
			if row.HasGuidance || !strings.HasPrefix(messages[i].Text, "Project instructions: further scopes omitted") {
				kept, keptRows = append(kept, messages[i]), append(keptRows, row)
				continue
			}
			if row.SourceID == "" {
				global = true
				continue
			}
			key := row.SourceID + "\x00" + messages[i].Text
			if seen[key] {
				continue
			}
			seen[key] = true
			remaining -= len(row.SourceID) + len("content") + 1
			notices, noticeRows = append(notices, messages[i]), append(noticeRows, row)
		}
		if global || remaining < 0 {
			notices = []session.Message{session.NewUserMessage("Project instructions: further scopes omitted (instruction budget exhausted).")}
			noticeRows = []InstructionManifest{{Kind: InstructionKindTurn0, Provenance: InstructionProvenanceProject}}
		}
		messages, rows = append(kept, notices...), append(keptRows, noticeRows...)
	}
	return messages, rows, nil
}
