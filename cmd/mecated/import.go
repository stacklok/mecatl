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
	fs.BoolVar(&includeSkills, "skills", false, "import conventional project and user skill bundles into <workspace>/.mecatl/skills")
	fs.Var(&skillSources, "skills-dir", "additional skill source laid out as <dir>/<name>/SKILL.md (repeatable)")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 0 || sourceName == "" || transcriptPath == "" || storeDir == "" {
		return errors.New("usage: mecated import --from <codex|claude-code> --session <transcript.jsonl> --store-dir <dir> [--workspace <dir>] [--copy-files] [--skills] [--skills-dir <dir>]")
	}
	source := agentimport.Source(sourceName)
	if source != agentimport.SourceCodex && source != agentimport.SourceClaudeCode {
		return fmt.Errorf("--from must be codex or claude-code, got %q", sourceName)
	}

	f, err := os.Open(transcriptPath) //nolint:gosec // explicit operator-selected import path
	if err != nil {
		return fmt.Errorf("open session transcript %q: %w", transcriptPath, err)
	}
	transcript, parseErr := agentimport.Parse(source, f)
	closeErr := f.Close()
	if parseErr != nil {
		return parseErr
	}
	if closeErr != nil {
		return fmt.Errorf("close session transcript %q: %w", transcriptPath, closeErr)
	}
	if len(transcript.Messages) == 0 {
		return fmt.Errorf("session transcript %q contains no importable user or assistant text", transcriptPath)
	}
	if sourceWorkspace == "" {
		sourceWorkspace = transcript.Workspace
	}
	workspaceWasExplicit := workspace != ""
	if workspace == "" {
		workspace = sourceWorkspace
	}
	if workspace == "" {
		return errors.New("the source transcript has no cwd; pass --workspace to set the Mecatl workspace")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("resolve target workspace: %w", err)
	}
	if copyFiles && !workspaceWasExplicit {
		return errors.New("--copy-files requires an explicit --workspace destination")
	}
	if (copyFiles || includeSkills) && sourceWorkspace == "" {
		return errors.New("the source transcript has no cwd; pass --source-workspace for file or project-skill import")
	}

	if id == "" {
		externalID := transcript.ExternalID
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
		return errors.New("--id must not be empty")
	}

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := store.Load(ctx, session.SessionID(id)); err == nil {
		return fmt.Errorf("Mecatl session %q already exists in %s; choose a different --id", id, storeDir)
	} else if !errors.Is(err, port.ErrSessionNotFound) {
		return fmt.Errorf("check destination session %q: %w", id, err)
	}

	var fileStats, skillStats agentimport.CopyStats
	if copyFiles {
		fileStats, err = agentimport.CopyWorkspace(sourceWorkspace, workspace)
		if err != nil {
			return err
		}
	}
	if includeSkills {
		skillSources = append(conventionalSkillSources(source, sourceWorkspace), skillSources...)
	}
	if len(skillSources) > 0 {
		skillDest := filepath.Join(workspace, ".mecatl", "skills")
		skillStats, err = agentimport.CopySkillSources(skillSources, skillDest)
		if err != nil {
			return err
		}
	}

	imported := session.New(session.SessionID(id), session.ModeDefault, workspace, session.Limits{}, transcript.CreatedAt)
	if err := imported.SeedHistory(transcript.Messages); err != nil {
		return fmt.Errorf("seed imported conversation: %w", err)
	}
	imported.SetTitle(transcript.Title)
	if err := store.Save(ctx, imported); err != nil {
		return fmt.Errorf("save imported session: %w", err)
	}
	_, _ = fmt.Fprintf(out, "Imported %s session %q: %d messages, %d workspace files, %d skills (%d symlinks skipped).\n", source, id, len(transcript.Messages), fileStats.Files, skillStats.Skills, fileStats.SkippedSymlinks+skillStats.SkippedSymlinks)
	_, _ = fmt.Fprintf(out, "Resume it with a Mecatl client connected to: mecated serve --store-dir %s --workspace %s\n", storeDir, workspace)
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
