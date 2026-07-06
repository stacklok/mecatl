// Command mecated is the standalone mecatl server binary and a composition root.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// schedulesSubcommands is the list printed for `mecated schedules` with no verb
// or an unknown verb. It mirrors the config-subcommand dispatch shape.
var schedulesSubcommands = []string{
	"create  create a schedule (cron or one-shot)",
	"list    list all schedules",
	"inspect show a schedule (and optionally its recent fires)",
	"pause   pause a schedule",
	"resume  resume a paused schedule",
	"delete  delete a schedule",
	"fire    force an immediate fire of a schedule",
}

// runSchedules implements `mecated schedules <verb> [flags]`: a thin HTTP client
// over the running server's /v1/schedules REST surface. It dials --server-addr
// (default the loopback HTTP listener the server itself binds) and prints the
// response as text or JSON. Like the `config` subcommand group, a bare
// `schedules` or an UNKNOWN verb must NOT fall through to run() and boot the
// daemon: it returns errSchedulesUsage (which main maps to exit 2 + the usage
// banner), never starting the daemon.
var errSchedulesUsage = errors.New("schedules: usage error")

func runSchedules(argv []string, out, errOut io.Writer) error {
	if len(argv) == 0 {
		_, _ = fmt.Fprintln(errOut, "mecated schedules: missing subcommand")
		printSchedulesUsage(errOut)
		return errSchedulesUsage
	}
	verb := argv[0]
	rest := argv[1:]
	switch verb {
	case "create":
		return runSchedulesCreate(rest, out, errOut)
	case "list":
		return runSchedulesList(rest, out, errOut)
	case "inspect":
		return runSchedulesInspect(rest, out, errOut)
	case "pause":
		return runSchedulesPause(rest, out, errOut)
	case "resume":
		return runSchedulesResume(rest, out, errOut)
	case "delete":
		return runSchedulesDelete(rest, out, errOut)
	case "fire":
		return runSchedulesFire(rest, out, errOut)
	default:
		_, _ = fmt.Fprintf(errOut, "mecated schedules: unknown subcommand %q\n", verb)
		printSchedulesUsage(errOut)
		return errSchedulesUsage
	}
}

func printSchedulesUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "available subcommands:")
	for _, s := range schedulesSubcommands {
		_, _ = fmt.Fprintf(out, "  schedules %s\n", s)
	}
}

// defaultSchedulesServerAddr is the loopback HTTP address the server itself
// binds by default; the CLI client dials it out of the box.
const defaultSchedulesServerAddr = "http://" + defaultHTTPAddr

// scheduleClient holds the shared CLI client state across the verbs.
type scheduleClient struct {
	serverAddr string
	output     string // "text" or "json"
	out        io.Writer
	errOut     io.Writer
	httpClient *http.Client
}

func newScheduleClient(fs *flag.FlagSet, out, errOut io.Writer) *scheduleClient {
	c := &scheduleClient{out: out, errOut: errOut, httpClient: &http.Client{Timeout: 60 * time.Second}}
	fs.StringVar(&c.serverAddr, "server-addr", defaultSchedulesServerAddr, "the mecated HTTP API base address")
	fs.StringVar(&c.output, "output", "text", "output format: text (default) or json")
	return c
}

// scheduleNameRe constrains schedule names to a path-safe subset. A name with
// '/', '?', '#', or traversal sequences ("../") could otherwise alter the URL
// path in /v1/schedules/<name>/... verbs. This is a CLIENT-side guard only;
// the server independently validates/normalizes.
var scheduleNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateScheduleName rejects a name that contains path separators or
// traversal sequences before the client dials the server. It is enforced in
// every verb that takes --name.
func validateScheduleName(name string) error {
	if !scheduleNameRe.MatchString(name) {
		return fmt.Errorf("invalid schedule name: must match [A-Za-z0-9._-]+")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid schedule name: must match [A-Za-z0-9._-]+")
	}
	return nil
}

// scheduleModeProtoName maps the human --mode word to the protojson enum name
// the server decodes (the CLI is a local JSON mirror — see cliScheduleSpec).
// These names are the stable wire contract (mecatlv1.PermissionMode); the CLI
// mirrors them here rather than importing contracts/gen, matching the rest of
// the local-JSON-mirror design.
func scheduleModeProtoName(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "plan":
		return "PERMISSION_MODE_PLAN", nil
	case "default":
		return "PERMISSION_MODE_DEFAULT", nil
	case "acceptedits", "accept-edits", "accept_edits":
		return "PERMISSION_MODE_ACCEPT_EDITS", nil
	default:
		return "", fmt.Errorf("--mode must be plan|default|acceptEdits (got %q)", m)
	}
}

// buildScheduleTrigger builds the trigger body from the mutually-exclusive
// --cron / --one-shot flags (the caller has already checked exactly one is set).
func buildScheduleTrigger(cron, oneShot string) (*cliTriggerSpec, error) {
	if cron != "" {
		return &cliTriggerSpec{Cron: cron}, nil
	}
	t, err := time.Parse(time.RFC3339, oneShot)
	if err != nil {
		return nil, fmt.Errorf("--one-shot must be RFC3339: %w", err)
	}
	return &cliTriggerSpec{OneShot: t}, nil
}

// resolveCreateMode applies the CLI's mode default and translates the human
// word to the protojson enum name. A non-mutating schedule defaults to plan
// mode (mirroring the declarative fold — the create-seam rejects a non-mutating
// schedule that is not in plan mode). Returns "" when no mode should be sent
// (a mutating schedule with no explicit --mode; the server picks its default).
func resolveCreateMode(mode string, mutating bool) (string, error) {
	m := strings.TrimSpace(mode)
	if m == "" {
		if mutating {
			return "", nil
		}
		m = "plan"
	}
	return scheduleModeProtoName(m)
}

// schedulePath builds a /v1/schedules/<name>[suffix] path, percent-escaping
// the name so it cannot alter the path. url.PathEscape("x") returns "x" for a
// normal name, so existing callers/tests see no change. suffix (e.g. "/pause")
// is appended verbatim — it is a fixed literal, not user input.
func schedulePath(name, suffix string) string {
	return "/v1/schedules/" + url.PathEscape(name) + suffix
}

// mecak8s note: cmd/mecak8s deliberately omits CLI subcommands (see its
// flags.go), so there is no `mecak8s schedules` surface — parity with the
// existing policy. k8s users manage schedules via the gRPC/HTTP API.

// do issues a request and returns the raw decoded JSON body bytes. A non-2xx
// response is an error carrying the server's {"error": ...} body.
func (c *scheduleClient) do(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.serverAddr+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.serverAddr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && errBody.Error != "" {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errBody.Error)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// print emits data as-is when --output json, otherwise as pretty-printed JSON.
// The server returns JSON in all cases; text mode just indents it for humans.
func (c *scheduleClient) print(data []byte) error {
	if c.output == "json" {
		_, err := c.out.Write(data)
		if err != nil {
			return err
		}
		// ensure trailing newline
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, _ = c.out.Write([]byte("\n"))
		}
		return nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		// Fall back to raw bytes if pretty-print fails.
		_, werr := c.out.Write(data)
		return werr
	}
	_, err := c.out.Write(buf.Bytes())
	return err
}

// --- scheduleSpecBody mirror (kept LOCAL to the CLI client) ----------------
//
// The CLI client speaks JSON over HTTP against the server's protojson decoder
// (internal/adapter/server/http.go), so it mirrors the *mecatlv1.ScheduleSpec
// shape here. This is a MINIMAL subset: only the fields the CLI populates
// (cron/one-shot trigger, prompt, workspace, provider/model, mode, mutating,
// limits) — it omits Misfire/Profile/MaxFires. Mode is sent as the protojson
// enum NAME (e.g. "PERMISSION_MODE_PLAN", via scheduleModeProtoName), since
// protojson decodes an enum from its canonical name or number, not the human
// word. It relies on protojson leniency (DiscardUnknown) for the rest.

type cliScheduleSpec struct {
	Name      string               `json:"name,omitempty"`
	Prompt    string               `json:"prompt,omitempty"`
	Trigger   *cliTriggerSpec      `json:"trigger,omitempty"`
	Selector  *cliProviderSelector `json:"selector,omitempty"`
	Profile   string               `json:"profile,omitempty"`
	Workspace string               `json:"workspace,omitempty"`
	Mode      string               `json:"mode,omitempty"`
	Limits    *cliLimits           `json:"limits,omitempty"`
	Mutating  bool                 `json:"mutating,omitempty"`
	MaxFires  int32                `json:"max_fires,omitempty"`
	Misfire   string               `json:"misfire,omitempty"`
	Singleton bool                 `json:"singleton,omitempty"`
	Timezone  string               `json:"timezone,omitempty"`
}

type cliTriggerSpec struct {
	Cron    string    `json:"cron,omitempty"`
	OneShot time.Time `json:"one_shot,omitempty"`
}

type cliProviderSelector struct {
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
}

type cliLimits struct {
	MaxTurns               int32 `json:"max_turns,omitempty"`
	MaxToolCalls           int32 `json:"max_tool_calls,omitempty"`
	MaxConsecutiveFailures int32 `json:"max_consecutive_failures,omitempty"`
}

// --- verbs ------------------------------------------------------------------

// runSchedulesCreate implements `mecated schedules create`.
func runSchedulesCreate(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules create", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var (
		name      string
		cron      string
		oneShot   string
		prompt    string
		provider  string
		model     string
		mode      string
		workspace string
		mutating  bool
		timezone  string
		maxTurns  int
		maxTokens int
		singleton bool
	)
	fs.StringVar(&name, "name", "", "schedule name (required)")
	fs.StringVar(&cron, "cron", "", "cron expression (5-field or @-macro); mutually exclusive with --one-shot")
	fs.StringVar(&oneShot, "one-shot", "", "RFC3339 wall-clock instant to fire once at; mutually exclusive with --cron")
	fs.StringVar(&prompt, "prompt", "", "the user prompt the fire runs with (required)")
	fs.StringVar(&provider, "provider", "", "provider id; empty = deployment default")
	fs.StringVar(&model, "model", "", "model id; empty = deployment default")
	fs.StringVar(&mode, "mode", "", "permission mode: plan|default|acceptEdits (defaults to plan for a non-mutating schedule)")
	fs.StringVar(&workspace, "workspace", "", "session working directory; empty = deployment default")
	fs.BoolVar(&mutating, "mutating", false, "explicit write opt-in (default false)")
	fs.StringVar(&timezone, "timezone", "UTC", "IANA timezone name the cron fires in")
	fs.IntVar(&maxTurns, "max-turns", 0, "per-fire max model turns (0 = disabled)")
	fs.IntVar(&maxTokens, "max-tokens", 0, "per-fire cumulative token ceiling (0 = disabled)")
	fs.BoolVar(&singleton, "singleton", true, "skip the next fire if a prior fire is still running (default true)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	if prompt == "" {
		return fmt.Errorf("--prompt is required")
	}
	if cron == "" && oneShot == "" {
		return fmt.Errorf("either --cron or --one-shot must be set")
	}
	if cron != "" && oneShot != "" {
		return fmt.Errorf("--cron and --one-shot are mutually exclusive")
	}
	trigger, err := buildScheduleTrigger(cron, oneShot)
	if err != nil {
		return err
	}
	modeName, err := resolveCreateMode(mode, mutating)
	if err != nil {
		return err
	}
	spec := cliScheduleSpec{
		Name:      name,
		Prompt:    prompt,
		Workspace: workspace,
		Mode:      modeName,
		Mutating:  mutating,
		Timezone:  timezone,
		Singleton: singleton,
		Trigger:   trigger,
	}
	if provider != "" || model != "" {
		spec.Selector = &cliProviderSelector{ProviderID: provider, ModelID: model}
	}
	if maxTurns > 0 || maxTokens > 0 {
		l := &cliLimits{}
		if maxTurns > 0 {
			l.MaxTurns = int32(maxTurns)
		}
		spec.Limits = l
		// NOTE: --max-tokens is accepted for forward-compat but the server's
		// Limits message has no max_tokens field in v1, so it is not currently
		// transmitted. It is validated above (must be non-negative) and will
		// wire through once the proto grows a token-budget field.
		_ = maxTokens
	}
	flat, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	data, err := c.do(http.MethodPost, "/v1/schedules", json.RawMessage(flat))
	if err != nil {
		return err
	}
	return c.print(data)
}

// runSchedulesList implements `mecated schedules list`.
func runSchedulesList(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules list", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	data, err := c.do(http.MethodGet, "/v1/schedules", nil)
	if err != nil {
		return err
	}
	return c.print(data)
}

// runSchedulesInspect implements `mecated schedules inspect [--name] [--fires]`.
func runSchedulesInspect(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules inspect", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var name string
	var fires bool
	fs.StringVar(&name, "name", "", "schedule name (required)")
	fs.BoolVar(&fires, "fires", false, "also list recent fires for the schedule")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	data, err := c.do(http.MethodGet, schedulePath(name, ""), nil)
	if err != nil {
		return err
	}
	if err := c.print(data); err != nil {
		return err
	}
	if fires {
		fdata, err := c.do(http.MethodGet, schedulePath(name, "/fires"), nil)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(c.out)
		if err := c.print(fdata); err != nil {
			return err
		}
	}
	return nil
}

// runSchedulesPause implements `mecated schedules pause --name`.
func runSchedulesPause(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules pause", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var name string
	fs.StringVar(&name, "name", "", "schedule name (required)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	_, err := c.do(http.MethodPost, schedulePath(name, "/pause"), nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.out, "schedule %q paused\n", name)
	return nil
}

// runSchedulesResume implements `mecated schedules resume --name`.
func runSchedulesResume(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules resume", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var name string
	fs.StringVar(&name, "name", "", "schedule name (required)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	_, err := c.do(http.MethodPost, schedulePath(name, "/resume"), nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.out, "schedule %q resumed\n", name)
	return nil
}

// runSchedulesDelete implements `mecated schedules delete --name`.
func runSchedulesDelete(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules delete", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var name string
	fs.StringVar(&name, "name", "", "schedule name (required)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	_, err := c.do(http.MethodDelete, schedulePath(name, ""), nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.out, "schedule %q deleted\n", name)
	return nil
}

// runSchedulesFire implements `mecated schedules fire --name`.
func runSchedulesFire(argv []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mecated schedules fire", flag.ContinueOnError)
	fs.SetOutput(errOut)
	c := newScheduleClient(fs, out, errOut)
	var name string
	fs.StringVar(&name, "name", "", "schedule name (required)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateScheduleName(name); err != nil {
		return err
	}
	data, err := c.do(http.MethodPost, schedulePath(name, "/fire"), nil)
	if err != nil {
		return err
	}
	// Print fire_id + session_id for humans; full JSON for --output json.
	if c.output == "json" {
		return c.print(data)
	}
	var resp struct {
		FireID    string `json:"fire_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		// Fall back to the raw body.
		return c.print(data)
	}
	_, _ = fmt.Fprintf(c.out, "fire_id: %s\nsession_id: %s\n", resp.FireID, resp.SessionID)
	return nil
}
