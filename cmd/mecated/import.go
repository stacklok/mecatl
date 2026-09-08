package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agentimport"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// runImport implements the offline `mecated import` command. It writes directly
// to the same local JSONL store used by `mecated serve --store-dir`, so no model
// credential or running daemon is required.
func runImport(argv []string, out io.Writer) error {
	req, err := buildImportRequest(argv, out)
	if err != nil {
		return err
	}
	return performImport(req, out)
}

// importRequest is the validated, id-minted plan for one import: everything
// performImport needs to apply the store/copy/save side effects, with no flag
// parsing or transcript I/O of its own.
type importRequest struct {
	source          agentimport.Source
	transcriptPath  string
	storeDir        string
	workspace       string
	sourceWorkspace string
	id              string
	copyFiles       bool
	includeSkills   bool
	skillSources    []string
	transcript      agentimport.Transcript
}

// buildImportRequest parses and validates the import flags, reads and parses
// the transcript, and mints the session id. It performs no store/copy/save side
// effects beyond opening and reading the transcript file.
func buildImportRequest(argv []string, out io.Writer) (importRequest, error) {
	flags, err := parseImportFlags(argv, out)
	if err != nil {
		return importRequest{}, err
	}
	transcript, err := loadTranscript(flags.source, flags.transcriptPath)
	if err != nil {
		return importRequest{}, err
	}
	workspace, sourceWorkspace, err := resolveImportWorkspace(flags, transcript)
	if err != nil {
		return importRequest{}, err
	}
	id, err := mintImportID(flags.id, flags.source, flags.transcriptPath, transcript.ExternalID)
	if err != nil {
		return importRequest{}, err
	}
	return importRequest{
		source:          flags.source,
		transcriptPath:  flags.transcriptPath,
		storeDir:        flags.storeDir,
		workspace:       workspace,
		sourceWorkspace: sourceWorkspace,
		id:              id,
		copyFiles:       flags.copyFiles,
		includeSkills:   flags.includeSkills,
		skillSources:    flags.skillSources,
		transcript:      transcript,
	}, nil
}

// importFlags holds the parsed import command-line flags.
type importFlags struct {
	source          agentimport.Source
	transcriptPath  string
	storeDir        string
	workspace       string
	sourceWorkspace string
	id              string
	copyFiles       bool
	includeSkills   bool
	skillSources    []string
}

func parseImportFlags(argv []string, out io.Writer) (importFlags, error) {
	fs := flag.NewFlagSet("mecated import", flag.ContinueOnError)
	fs.SetOutput(out)
	var sourceName, transcriptPath, storeDir, workspace, sourceWorkspace, id string
	var copyFiles, includeSkills bool
	var skillSources stringList
	fs.StringVar(&sourceName, "from", "", "external agent format: codex or claude-code")
	fs.StringVar(&transcriptPath, "session", "", "path to the source session JSONL transcript")
	fs.StringVar(&storeDir, "store-dir", "", "destination Mecatl JSONL session store (use the same value with mecated serve)")
	fs.StringVar(&workspace, "workspace", "", "target Mecatl workspace (default: the source session cwd)")
	fs.StringVar(&sourceWorkspace, "source-workspace", "", "source project directory for --copy-files and project skills (default: the session cwd)")
	fs.StringVar(&id, "id", "", "Mecatl session id (default: import-<source>-<external-id>)")
	fs.BoolVar(&copyFiles, "copy-files", false, "copy regular workspace files into --workspace without overwriting existing paths (.git and symlinks are skipped)")
	fs.BoolVar(&includeSkills, "skills", false, "import conventional project and user skill bundles into <workspace>/.mecatl/skills. TRUST BOUNDARY: imported SKILL.md files steer the model like AGENTS.md/CLAUDE.md — only import from trusted sources")
	fs.Var(&skillSources, "skills-dir", "additional skill source laid out as <dir>/<name>/SKILL.md (repeatable). TRUST BOUNDARY: a SKILL.md steers the model like AGENTS.md/CLAUDE.md — point this only at directories you trust")
	if err := fs.Parse(argv); err != nil {
		return importFlags{}, err
	}
	if fs.NArg() != 0 || sourceName == "" || transcriptPath == "" || storeDir == "" {
		return importFlags{}, errors.New("usage: mecated import --from <codex|claude-code> --session <transcript.jsonl> --store-dir <dir> [--workspace <dir>] [--copy-files] [--skills] [--skills-dir <dir>]")
	}
	source := agentimport.Source(sourceName)
	if source != agentimport.SourceCodex && source != agentimport.SourceClaudeCode {
		return importFlags{}, fmt.Errorf("--from must be codex or claude-code, got %q", sourceName)
	}
	return importFlags{
		source:          source,
		transcriptPath:  transcriptPath,
		storeDir:        storeDir,
		workspace:       workspace,
		sourceWorkspace: sourceWorkspace,
		id:              id,
		copyFiles:       copyFiles,
		includeSkills:   includeSkills,
		skillSources:    skillSources,
	}, nil
}

func loadTranscript(source agentimport.Source, transcriptPath string) (agentimport.Transcript, error) {
	f, err := os.Open(transcriptPath) //nolint:gosec // explicit operator-selected import path
	if err != nil {
		return agentimport.Transcript{}, fmt.Errorf("open session transcript %q: %w", transcriptPath, err)
	}
	transcript, parseErr := agentimport.Parse(source, f)
	closeErr := f.Close()
	if parseErr != nil {
		return agentimport.Transcript{}, parseErr
	}
	if closeErr != nil {
		return agentimport.Transcript{}, fmt.Errorf("close session transcript %q: %w", transcriptPath, closeErr)
	}
	if len(transcript.Messages) == 0 {
		return agentimport.Transcript{}, fmt.Errorf("session transcript %q contains no importable user or assistant text", transcriptPath)
	}
	return transcript, nil
}

// resolveImportWorkspace derives the effective workspace and source-workspace,
// applying the explicit-workspace requirement for --copy-files and the cwd
// requirement for file/skill import.
func resolveImportWorkspace(flags importFlags, transcript agentimport.Transcript) (workspace, sourceWorkspace string, err error) {
	sourceWorkspace = flags.sourceWorkspace
	if sourceWorkspace == "" {
		sourceWorkspace = transcript.Workspace
	}
	workspace = flags.workspace
	workspaceWasExplicit := workspace != ""
	if workspace == "" {
		workspace = sourceWorkspace
	}
	if workspace == "" {
		return "", "", errors.New("the source transcript has no cwd; pass --workspace to set the Mecatl workspace")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve target workspace: %w", err)
	}
	if flags.copyFiles && !workspaceWasExplicit {
		return "", "", errors.New("--copy-files requires an explicit --workspace destination")
	}
	if (flags.copyFiles || flags.includeSkills) && sourceWorkspace == "" {
		return "", "", errors.New("the source transcript has no cwd; pass --source-workspace for file or project-skill import")
	}
	return workspace, sourceWorkspace, nil
}

func mintImportID(id string, source agentimport.Source, transcriptPath, externalID string) (string, error) {
	if id == "" {
		if externalID == "" {
			sum := sha256.Sum256([]byte(transcriptPath))
			externalID = fmt.Sprintf("%x", sum[:8])
		}
		part := safeIDPart(externalID)
		if part == "" {
			sum := sha256.Sum256([]byte(externalID))
			part = fmt.Sprintf("%x", sum[:8])
		}
		id = "import-" + string(source) + "-" + part
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("--id must not be empty")
	}
	return id, nil
}

// performImport applies the validated request: it opens the store, copies the
// workspace and skill bundles, seeds the session, saves it, and writes the
// human-readable summary to out.
func performImport(req importRequest, out io.Writer) error {
	store, err := jsonlstore.New(req.storeDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := store.Load(ctx, session.SessionID(req.id)); err == nil {
		return fmt.Errorf("mecatl session %q already exists in %s; choose a different --id", req.id, req.storeDir)
	} else if !errors.Is(err, port.ErrSessionNotFound) {
		return fmt.Errorf("check destination session %q: %w", req.id, err)
	}

	var fileStats, skillStats agentimport.CopyStats
	if req.copyFiles {
		fileStats, err = agentimport.CopyWorkspace(req.sourceWorkspace, req.workspace)
		if err != nil {
			return err
		}
	}
	skillSources := req.skillSources
	if req.includeSkills {
		skillSources = append(conventionalSkillSources(req.source, req.sourceWorkspace), skillSources...)
	}
	if len(skillSources) > 0 {
		skillDest := filepath.Join(req.workspace, ".mecatl", "skills")
		skillStats, err = agentimport.CopySkillSources(skillSources, skillDest)
		if err != nil {
			return err
		}
	}

	imported := session.New(session.SessionID(req.id), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: req.workspace, Revision: "in-tree-v1"}, session.Limits{}, req.transcript.CreatedAt)
	if err := imported.SeedHistory(req.transcript.Messages); err != nil {
		return fmt.Errorf("seed imported conversation: %w", err)
	}
	imported.SetTitle(req.transcript.Title)
	if err := store.Save(ctx, imported); err != nil {
		return fmt.Errorf("save imported session: %w", err)
	}
	_, _ = fmt.Fprintf(out, "Imported %s session %q: %d messages, %d workspace files, %d skills (%d symlinks skipped).\n", req.source, req.id, len(req.transcript.Messages), fileStats.Files, skillStats.Skills, fileStats.SkippedSymlinks+skillStats.SkippedSymlinks)
	_, _ = fmt.Fprintf(out, "Resume it with a Mecatl client connected to: mecated serve --store-dir %s --workspace %s\n", req.storeDir, req.workspace)
	return nil
}

func conventionalSkillSources(source agentimport.Source, sourceWorkspace string) []string {
	var dirs []string
	if sourceWorkspace != "" {
		switch source {
		case agentimport.SourceCodex:
			dirs = append(dirs, filepath.Join(sourceWorkspace, ".agents", "skills"), filepath.Join(sourceWorkspace, ".codex", "skills"))
		case agentimport.SourceClaudeCode:
			dirs = append(dirs, filepath.Join(sourceWorkspace, ".claude", "skills"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		switch source {
		case agentimport.SourceCodex:
			dirs = append(dirs, filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".codex", "skills"))
		case agentimport.SourceClaudeCode:
			dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
		}
	}
	return dirs
}

func safeIDPart(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
			lastDash = r == '-'
		} else if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 96 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
