// Package microvmcmd implements the binary-neutral, local microVM lifecycle
// administration command shared by mecated and mecatui.
package microvmcmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

// Manager is the narrow lifecycle-administration surface used by the command.
type Manager interface {
	Doctor(context.Context) (string, error)
	Status(context.Context, ...microvmmanager.StatusRequest) (microvmmanager.Status, error)
	Delete(context.Context, microvmmanager.DeleteRequest) (microvmmanager.DeleteResult, error)
}

type outputFormat string

// Frontend identifies the installed binary that invoked the shared command.
type Frontend string

const (
	// FrontendMecated identifies the mecated command frontend.
	FrontendMecated Frontend = "mecated"
	// FrontendMecatui identifies the mecatui command frontend.
	FrontendMecatui Frontend = "mecatui"

	outputText outputFormat = "text"
	outputJSON outputFormat = "json"
)

// Run parses and executes one local microVM administration command.
func Run(ctx context.Context, frontend Frontend, args []string, in io.Reader, out io.Writer, manager Manager, interactive bool) error {
	if frontend != FrontendMecated && frontend != FrontendMecatui {
		return fmt.Errorf("unsupported microvm command frontend %q", frontend)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "--help-all" || args[0] == "-h" {
		WriteHelp(out)
		return nil
	}
	switch args[0] {
	case "doctor":
		return runDoctor(ctx, args[1:], out, manager)
	case "status":
		return runStatus(ctx, frontend, args[1:], out, manager)
	case "delete":
		return runDelete(ctx, args[1:], in, out, manager, interactive)
	default:
		return fmt.Errorf("unknown microvm command %q; run 'mecated microvm --help' (or 'mecatui microvm --help') for doctor, status, and delete usage", args[0])
	}
}

func outputFlag(fs *flag.FlagSet, value *string) {
	fs.StringVar(value, "output", string(outputText), "output format: text or json")
}

func parseOutput(value string) (outputFormat, error) {
	format := outputFormat(value)
	if format != outputText && format != outputJSON {
		return "", fmt.Errorf("unknown output format %q; use text or json", value)
	}
	return format, nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func runDoctor(ctx context.Context, args []string, out io.Writer, manager Manager) error {
	fs := flag.NewFlagSet("microvm doctor", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { writeDoctorHelp(out) }
	var output string
	outputFlag(fs, &output)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: mecated microvm doctor [--output text|json]")
	}
	format, err := parseOutput(output)
	if err != nil {
		return err
	}
	report, doctorErr := manager.Doctor(ctx)
	if format == outputJSON {
		result := struct {
			Success bool   `json:"success"`
			Report  string `json:"report"`
			Error   string `json:"error"`
		}{Success: doctorErr == nil, Report: report}
		if doctorErr != nil {
			result.Error = doctorErr.Error()
		}
		if err := writeJSON(out, result); err != nil {
			return err
		}
	} else if report != "" {
		_, _ = io.WriteString(out, report)
		if !strings.HasSuffix(report, "\n") {
			_, _ = fmt.Fprintln(out)
		}
	}
	if doctorErr != nil {
		return fmt.Errorf("microVM doctor found problems: %w; follow the next action in the report, then rerun 'mecated microvm doctor' (or 'mecatui microvm doctor')", doctorErr)
	}
	return nil
}

func runStatus(ctx context.Context, frontend Frontend, args []string, out io.Writer, manager Manager) error {
	fs := flag.NewFlagSet("microvm status", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { writeStatusHelp(out) }
	var pageSize int
	var continuation, output string
	fs.IntVar(&pageSize, "page-size", 50, "generation rows per page (maximum 64)")
	fs.StringVar(&continuation, "continuation", "", "opaque continuation from the previous status page")
	outputFlag(fs, &output)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || pageSize <= 0 || pageSize > 64 || len(continuation) > 1024 {
		return errors.New("usage: mecated microvm status [--page-size 1..64] [--continuation TOKEN] [--output text|json]")
	}
	format, err := parseOutput(output)
	if err != nil {
		return err
	}
	status, err := manager.Status(ctx, microvmmanager.StatusRequest{PageSize: pageSize, Continuation: continuation})
	if err != nil {
		return fmt.Errorf("inspect microVM status: %w", err)
	}
	if format == outputJSON {
		return writeJSON(out, statusJSON{
			Profile: microvmmanager.Alias, Configured: status.Configured, Running: status.Running,
			Socket: status.Socket, GuestEgress: status.GuestEgress, Generations: generationJSONs(status.Generations), Continuation: status.Continuation,
		})
	}
	writeStatusText(out, frontend, status)
	return nil
}

type generationJSON struct {
	SessionID     string `json:"session_id"`
	EnvironmentID string `json:"environment_id"`
	Ref           string `json:"ref"`
	Generation    uint32 `json:"generation"`
	WorktreePath  string `json:"worktree_path"`
	State         string `json:"state"`
	Health        string `json:"health"`
	Error         string `json:"error"`
}

type statusJSON struct {
	Profile      string           `json:"profile"`
	Configured   bool             `json:"configured"`
	Running      bool             `json:"running"`
	Socket       string           `json:"socket"`
	GuestEgress  string           `json:"guest_egress"`
	Generations  []generationJSON `json:"generations"`
	Continuation string           `json:"continuation"`
}

func generationJSONs(generations []microvmmanager.Generation) []generationJSON {
	result := make([]generationJSON, len(generations))
	for i, generation := range generations {
		result[i] = generationJSON{
			SessionID: generation.SessionID, EnvironmentID: generation.EnvironmentID, Ref: generation.Ref,
			Generation: generation.Generation, WorktreePath: generation.WorktreePath, State: generation.State,
			Health: string(generation.Health), Error: generation.Error,
		}
	}
	return result
}

func writeStatusText(out io.Writer, frontend Frontend, status microvmmanager.Status) {
	_, _ = fmt.Fprintf(out, "profile: %s\nconfigured: %t\ndaemon running: %t\nsocket: %s\nguest egress: %s\n", microvmmanager.Alias, status.Configured, status.Running, status.Socket, configuredValue(status.GuestEgress))
	for _, generation := range status.Generations {
		_, _ = fmt.Fprintf(out, "logical worktree: session=%s ref=%s repository-generation=%d health=%s state=%s worktree=%s", generation.SessionID, generation.Ref, generation.Generation, generation.Health, generation.State, generation.WorktreePath)
		if generation.Error != "" {
			_, _ = fmt.Fprintf(out, " error=%q", generation.Error)
		}
		_, _ = fmt.Fprintln(out)
	}
	if status.Continuation != "" {
		_, _ = fmt.Fprintf(out, "next page: %s microvm status --continuation %q\n", frontend, status.Continuation)
	}
	_, _ = fmt.Fprintln(out, "status is read-only; it never installs, starts, stops, or deletes microVM state")
	if !status.Configured {
		_, _ = fmt.Fprintln(out, "backend state: not configured")
		writeSelectionExamples(out)
	} else if !status.Running {
		_, _ = fmt.Fprintln(out, "backend state: configured; daemon not running")
		writeSelectionExamples(out)
	}
}

func configuredValue(value string) string {
	if value == "" {
		return "not configured"
	}
	return value
}

func writeSelectionExamples(out io.Writer) {
	_, _ = fmt.Fprintln(out, "next (mecatui): mecatui --default-placement microvm-local")
	_, _ = fmt.Fprintln(out, `next (HTTP API): run 'mecated serve --headless --default-placement microvm-local', then POST /v1/sessions with {}`)
}

func runDelete(ctx context.Context, args []string, in io.Reader, out io.Writer, manager Manager, interactive bool) error {
	fs := flag.NewFlagSet("microvm delete", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { writeDeleteHelp(out) }
	var yes bool
	var sessionID, ref, output string
	var generation uint
	fs.BoolVar(&yes, "yes", false, "confirm destructive removal")
	fs.StringVar(&sessionID, "session", "", "exact session id")
	fs.StringVar(&ref, "ref", "", "exact microVM environment ref")
	fs.UintVar(&generation, "generation", 0, "exact microVM generation")
	outputFlag(fs, &output)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("microvm delete: unexpected argument %q", fs.Arg(0))
	}
	format, err := parseOutput(output)
	if err != nil {
		return err
	}
	if uint64(generation) > uint64(^uint32(0)) {
		return errors.New("microvm delete: generation exceeds uint32")
	}
	request := microvmmanager.DeleteRequest{SessionID: sessionID, Ref: ref, Generation: uint32(generation)} // #nosec G115 -- range checked above.
	if err := microvmmanager.ValidateDeleteRequest(request); err != nil {
		return err
	}
	found, err := statusHasGeneration(ctx, manager, request)
	if err != nil {
		return fmt.Errorf("validate microVM delete target: %w", err)
	}
	if !found {
		return errors.New("microVM delete target is not present in microvm status; refresh status and copy all three values from one row")
	}
	if format == outputJSON && !yes {
		return errors.New("microVM delete with --output json requires explicit confirmation with --yes")
	}
	if format == outputText {
		_, _ = fmt.Fprintf(out, "Permanently delete logical worktree session=%s ref=%s repository-generation=%d. The shared repository VM is not deleted. Closing a host client normally only detaches and preserves this worktree. Dirty worktrees are retained.\n", sessionID, ref, generation)
		if interactive && !yes {
			_, _ = fmt.Fprint(out, "Permanently delete this exact microVM generation? [y/N] ")
			answer, _ := bufio.NewReader(in).ReadString('\n')
			yes = strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
		}
	}
	if !yes {
		return errors.New("microVM delete requires confirmation (--yes for noninteractive use)")
	}
	result, err := manager.Delete(ctx, request)
	if err != nil {
		return fmt.Errorf("delete exact microVM generation: %w", err)
	}
	if format == outputJSON {
		return writeJSON(out, struct {
			Selector      deleteSelectorJSON `json:"selector"`
			Result        deleteResultJSON   `json:"result"`
			DirtyRetained bool               `json:"dirty_retained"`
		}{
			Selector:      deleteSelectorJSON{SessionID: request.SessionID, Ref: request.Ref, Generation: request.Generation},
			Result:        deleteResultJSON{WorktreePath: result.WorktreePath, WorktreeRemoved: !result.WorktreeRetained, RepositoryVMRetained: true},
			DirtyRetained: result.WorktreeRetained,
		})
	}
	if result.WorktreeRetained {
		_, _ = fmt.Fprintf(out, "logical attachment deleted; dirty worktree retained: %s\n", result.WorktreePath)
	} else {
		_, _ = fmt.Fprintf(out, "logical attachment and clean worktree removed; repository VM retained: %s\n", result.WorktreePath)
	}
	return nil
}

type deleteSelectorJSON struct {
	SessionID  string `json:"session_id"`
	Ref        string `json:"ref"`
	Generation uint32 `json:"generation"`
}

type deleteResultJSON struct {
	WorktreePath         string `json:"worktree_path"`
	WorktreeRemoved      bool   `json:"worktree_removed"`
	RepositoryVMRetained bool   `json:"repository_vm_retained"`
}

func statusHasGeneration(ctx context.Context, manager Manager, request microvmmanager.DeleteRequest) (bool, error) {
	continuation := ""
	seen := make(map[string]struct{})
	for range 1024 {
		status, err := manager.Status(ctx, microvmmanager.StatusRequest{PageSize: 64, Continuation: continuation})
		if err != nil {
			return false, err
		}
		for _, generation := range status.Generations {
			if generation.SessionID == request.SessionID && generation.Ref == request.Ref && generation.Generation == request.Generation {
				return true, nil
			}
		}
		if status.Continuation == "" {
			return false, nil
		}
		if _, duplicate := seen[status.Continuation]; duplicate {
			return false, errors.New("microVM status returned a repeated continuation")
		}
		seen[status.Continuation] = struct{}{}
		continuation = status.Continuation
	}
	return false, errors.New("microVM delete validation exceeded 1024 status pages")
}

// WriteHelp documents the canonical command and the compatibility frontend.
func WriteHelp(out io.Writer) {
	lines := []string{
		"Usage: mecated microvm doctor|status|delete [flags]",
		"       mecatui microvm doctor|status|delete [flags]",
		"",
		"Administers microVM state owned by the current local OS principal on this host.",
		"It never targets a remote server selected by 'mecatui connect'.",
		"'mecatui microvm' is a compatibility frontend over this same command.",
		"",
		"doctor    read-only host preflight and backend health; never installs or starts",
		"status    read-only owner-scoped repository generations and logical worktrees",
		"delete    remove an exact logical --session/--ref/--generation after validation and confirmation",
		"",
		"Run '<binary> microvm <command> --help' for command flags and examples.",
		"Text output is the default; every command accepts --output text|json.",
		"Repository-VM deletion is not supported.",
		"",
		"Create a microVM session in mecatui:",
		"  mecatui --default-placement microvm-local",
		"Create one through a headless HTTP server (child asks use headless policy, not a local TUI):",
		`  mecated serve --headless --default-placement microvm-local; then POST /v1/sessions with {}`,
		"Selecting the deployment default performs installation/readiness; doctor and status never do.",
	}
	_, _ = fmt.Fprintln(out, strings.Join(lines, "\n"))
}

func writeDoctorHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, `Usage: mecated microvm doctor [--output text|json]
       mecatui microvm doctor [--output text|json]

Read-only host preflight and backend diagnosis. This command never downloads,
installs, configures, or starts microvmd. On a fresh home it reports "not
configured" and shows how selecting microvm-local performs readiness.`)
}

func writeStatusHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, `Usage: mecated microvm status [--page-size 1..64] [--continuation TOKEN] [--output text|json]
       mecatui microvm status [--page-size 1..64] [--continuation TOKEN] [--output text|json]

Read-only owner-scoped backend and logical-worktree inventory. It never installs,
starts, stops, or deletes anything. Pass the printed opaque continuation token to
read the next page.`)
}

func writeDeleteHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, `Usage: mecated microvm delete --session ID --ref REF --generation N [--yes] [--output text|json]
       mecatui microvm delete --session ID --ref REF --generation N [--yes] [--output text|json]

Copy all three selector values from one 'microvm status' row. This removes only
the exact logical attachment and a clean worktree; dirty worktrees and the shared
repository VM are retained. Interactive text mode prompts unless --yes is set;
noninteractive and JSON use require --yes.`)
}
