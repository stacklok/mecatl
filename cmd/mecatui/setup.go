package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const (
	maxSetupAPIKeyBytes      = 8 * 1024
	credentialSourceAuthFile = "auth file"
	credentialSourceMissing  = "missing"
	openAIProviderID         = "openai"
	openCodeProviderID       = "opencode"
	codexProviderID          = "openai-codex"
	credentialLocallyUsable  = "locally usable"
	codexSetupGuidance       = "Existing Codex OAuth credentials in auth.yaml; interactive sign-in/refresh not supported. Configure or replace the manual token there and restart; setup does not collect, import, remove, or revoke it. See https://mecatl.dev/docs/building/deployment/mecatui#experimental-chatgpt-codex-subscription"
)

var errSetupStarted = errors.New("setup launched mecatui")

type providerStatus struct {
	ID, Kind, CredentialSource, Model, DefaultModel string
	LocalCredential                                 string
	FilePresent, Default, Mutable                   bool
}
type modelChoice struct {
	ID, Name    string
	ToolCapable bool
}
type defaultSelection struct{ Provider, Model string }
type setupSnapshot struct {
	Rows             []providerStatus
	NativeIDs        []string
	NativeDefaults   map[string]string
	ToolHive         bool
	Default          defaultSelection
	Aliases          map[string]string
	Models           map[string][]modelChoice
	ProviderDefaults map[string]string
	ExplicitMissing  bool
}

type setupDeps struct {
	load           func(string, bool) (setupSnapshot, error)
	preflight      func(string, string) error
	writeSupported func() bool
	isTerminal     func(int) bool
	readPassword   func(int) ([]byte, error)
	readSecret     func(context.Context) ([]byte, error)
	updateKey      func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error)
	updateDefaults func(context.Context, string, defaultSelection) (authfile.CommitState, error)
	nativeLogin    func(context.Context, string) error
	nativeUsable   func(context.Context, string) (bool, error)
	start          func(string) error
}

func defaultSetupDeps() setupDeps {
	return setupDepsForRun(run)
}

func setupDepsForRun(runCommand func([]string) error) setupDeps {
	return setupDeps{
		load:           loadSetupSnapshot,
		preflight:      preflightSetupDocuments,
		writeSupported: authfile.UpdateSupported,
		isTerminal:     term.IsTerminal,
		readPassword:   term.ReadPassword,
		updateKey:      authfile.UpdateAPIKey,
		updateDefaults: func(ctx context.Context, path string, d defaultSelection) (authfile.CommitState, error) {
			return permconfig.UpdateDefaults(ctx, path, permconfig.DefaultUpdate{Provider: d.Provider, Model: d.Model})
		},
		start: func(path string) error { return runCommand([]string{"mecatui", "--auth-file", path}) },
	}
}

func withSetupDefaults(deps setupDeps) setupDeps {
	defaults := defaultSetupDeps()
	if deps.load == nil {
		deps.load = defaults.load
	}
	if deps.preflight == nil {
		deps.preflight = defaults.preflight
	}
	if deps.writeSupported == nil {
		deps.writeSupported = defaults.writeSupported
	}
	if deps.isTerminal == nil {
		deps.isTerminal = defaults.isTerminal
	}
	if deps.readPassword == nil {
		deps.readPassword = defaults.readPassword
	}
	if deps.updateKey == nil {
		deps.updateKey = defaults.updateKey
	}
	if deps.updateDefaults == nil {
		deps.updateDefaults = defaults.updateDefaults
	}
	if deps.start == nil {
		deps.start = defaults.start
	}
	return deps
}

func settingsPath(env xdgconfig.ResolveEnv) string {
	base := xdgconfig.UserConfigDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, permconfig.UserSettingsRelPath)
}

//nolint:gocyclo // One passive snapshot keeps provider/default/provenance folding together.
func loadSetupSnapshot(path string, explicit bool) (setupSnapshot, error) {
	env := xdgconfig.OSEnv
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true, ImportClaude: true, Diagnostics: port.NopDiagnostics{}}, env)
	defs, _, err := resolver.OperatorProviders()
	if err != nil {
		return setupSnapshot{}, fmt.Errorf("resolve operator providers: %w", err)
	}
	known := []string{"anthropic", openAIProviderID, "openrouter", openCodeProviderID, "openai-codex"}
	for id := range defs {
		known = append(known, id)
	}
	explicitMissing := false
	if explicit {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			explicitMissing = true
		}
	}
	af, warning := authfile.Load(path, explicit && !explicitMissing, env, known)
	if warning != "" {
		return setupSnapshot{}, errors.New(safeDisplay(warning))
	}
	s := setupSnapshot{
		Models: map[string][]modelChoice{}, Aliases: map[string]string{}, ProviderDefaults: map[string]string{}, NativeDefaults: map[string]string{},
		ToolHive: app.ToolhiveConfiguredPassive(""), ExplicitMissing: explicitMissing,
	}
	models := resolver.OperatorModelPolicy()
	if models != nil {
		s.Default = defaultSelection{Provider: models.DefaultProvider, Model: models.Default}
		for k, v := range models.Aliases {
			s.Aliases[k] = v
		}
	}
	filePresent := func(id string) bool { return af != nil && af.APIKey(id) != "" }
	for _, id := range []string{"anthropic", openAIProviderID, openCodeProviderID, "openrouter"} {
		cs := cliconfig.ResolveCredentialSource(id, filePresent(id), os.Getenv)
		providerDefault := app.BuiltinDefaultModel(id)
		s.ProviderDefaults[id] = providerDefault
		model := providerDefault
		if s.Default.Provider == id && s.Default.Model != "" {
			model = s.Default.Model
		}
		s.Rows = append(s.Rows, providerStatus{ID: id, Kind: "builtin", CredentialSource: cs.Source, FilePresent: cs.FilePresent, Default: s.Default.Provider == id, Model: model, DefaultModel: providerDefault, Mutable: true})
		if p, ok := providercatalog.Default().Provider(id); ok {
			for _, m := range p.Models() {
				s.Models[id] = append(s.Models[id], modelChoice{ID: m.ID(), Name: m.Name(), ToolCapable: m.SupportsToolCall()})
			}
		}
	}
	codex := providerStatus{ID: codexProviderID, Kind: "manual OAuth", CredentialSource: credentialSourceMissing, LocalCredential: credentialSourceMissing, Default: s.Default.Provider == codexProviderID}
	if codex.Default {
		codex.Model = s.Default.Model
	}
	oauth := af.OAuth(codexProviderID)
	if oauth.AccessToken != "" {
		codex.FilePresent = true
		codex.CredentialSource = credentialSourceAuthFile
		codex.LocalCredential = "invalid or expired"
		if _, err := openaicodex.NewCredential(oauth.AccessToken, oauth.AccountID, oauth.ExpiresAt, time.Now()); err == nil {
			codex.LocalCredential = credentialLocallyUsable
		}
	}
	s.Rows = append(s.Rows, codex)
	for id, def := range defs {
		s.ProviderDefaults[id] = def.DefaultModel
		if def.Native != nil {
			s.NativeIDs = append(s.NativeIDs, id)
			s.NativeDefaults[id] = def.DefaultModel
			continue
		}
		if _, builtin := slices.BinarySearch([]string{"anthropic", openAIProviderID, openCodeProviderID, "openrouter"}, id); builtin {
			continue
		}
		kind := "none required"
		mutable := false
		source := "none required"
		if def.Auth.Method == "api_key" {
			kind = "configured"
			mutable = true
			if filePresent(id) {
				source = credentialSourceAuthFile
			} else {
				source = credentialSourceMissing
			}
		}
		model := def.DefaultModel
		if s.Default.Provider == id && s.Default.Model != "" {
			model = s.Default.Model
		}
		s.Rows = append(s.Rows, providerStatus{ID: id, Kind: kind, CredentialSource: source, FilePresent: filePresent(id), Default: s.Default.Provider == id, Model: model, DefaultModel: def.DefaultModel, Mutable: mutable})
	}
	slices.SortFunc(s.Rows, func(a, b providerStatus) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(s.NativeIDs)
	return s, nil
}

//nolint:errcheck // Status rendering cannot recover from a failed command output stream.
func writeAggregateStatus(out io.Writer, rows []providerStatus, native []string, nativeDefaults map[string]string, toolhive bool) {
	rows = append([]providerStatus(nil), rows...)
	slices.SortFunc(rows, func(a, b providerStatus) int { return strings.Compare(a.ID, b.ID) })
	fmt.Fprintln(out, "Keyed providers:")
	writeRows := func(kind string) {
		for _, r := range rows {
			if r.ID == codexProviderID || (kind == "keyed") != r.Mutable {
				continue
			}
			shadowed := "absent"
			if r.FilePresent && r.CredentialSource != credentialSourceAuthFile {
				shadowed = "present"
			}
			fmt.Fprintf(out, "  %s (%s)\n    credential source: %s\n    shadowed auth file: %s\n    selected default: %s\n    model selector: %s\n    verification: not checked\n", providerLabel(r.ID), safeDisplay(r.Kind), safeDisplay(r.CredentialSource), shadowed, yesNo(r.Default), displayOrNone(r.Model))
		}
	}
	writeRows("keyed")
	fmt.Fprintln(out, "No credential required:")
	writeRows("none")
	fmt.Fprintln(out, "Subscription tokens:")
	for _, r := range rows {
		if r.ID == codexProviderID {
			fmt.Fprintf(out, "  %s (%s)\n    credential source: %s\n    local credential: %s\n    selected default: %s\n    model selector: %s\n    verification: not checked (account/model entitlement unverified)\n    %s\n", providerLabel(r.ID), safeDisplay(r.Kind), safeDisplay(r.CredentialSource), safeDisplay(r.LocalCredential), yesNo(r.Default), displayOrNone(r.Model), codexSetupGuidance)
		}
	}
	fmt.Fprintln(out, "Native endpoints:")
	if len(native) == 0 {
		fmt.Fprintln(out, "  none configured")
	} else {
		for _, id := range native {
			fmt.Fprintf(out, "  %s: local enrollment not inspected; declared default: %s; run `mecatui llm status %s`\n", safeDisplay(id), displayOrNone(nativeDefaults[id]), safeDisplay(id))
		}
	}
	fmt.Fprintln(out, "ToolHive:")
	if toolhive {
		fmt.Fprintln(out, "  lifecycle owned by ToolHive; use `thv llm` tooling (not checked)")
	} else {
		fmt.Fprintln(out, "  not configured (not checked)")
	}
}

func providerLabel(id string) string {
	switch id {
	case openAIProviderID:
		return "openai — OpenAI (API key)"
	case codexProviderID:
		return "openai-codex — OpenAI Codex (existing subscription token)"
	default:
		return safeDisplay(id)
	}
}

func safeDisplay(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(v, "�"))
}
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
func displayOrNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "none"
	}
	return safeDisplay(v)
}

func runAggregateStatus(path string, explicit bool, out io.Writer, deps setupDeps) error {
	if deps.load == nil {
		deps.load = loadSetupSnapshot
	}
	s, err := deps.load(path, explicit)
	if err != nil {
		return err
	}
	label := "Auth file: conventional path (absence is normal)"
	if s.ExplicitMissing {
		label = "Auth file: explicit path is missing"
	} else if explicit {
		label = "Auth file: explicit path loaded"
	}
	if _, err := fmt.Fprintln(out, label); err != nil {
		return err
	}
	writeAggregateStatus(out, s.Rows, s.NativeIDs, s.NativeDefaults, s.ToolHive)
	return nil
}

func mutableProviderIDs(s setupSnapshot) []string {
	var ids []string
	for _, r := range s.Rows {
		if r.Mutable {
			ids = append(ids, r.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func handoffNative(ctx context.Context, id string, override, confirmed bool, out io.Writer, deps setupDeps) error {
	if override {
		return errors.New("native login does not use --auth-file; run `mecatui llm login " + safeDisplay(id) + "` separately")
	}
	if !confirmed {
		return nil
	}
	if deps.nativeLogin == nil {
		return errors.New("native LLM endpoint lifecycle is unavailable")
	}
	if _, err := fmt.Fprintln(out, "Handing off to native endpoint login."); err != nil {
		return err
	}
	return deps.nativeLogin(ctx, id)
}

func setupModelChoices(existing string, catalog []modelChoice) []modelChoice {
	out := []modelChoice{}
	seen := map[string]bool{}
	if strings.TrimSpace(existing) != "" {
		out = append(out, modelChoice{ID: existing, Name: "current default", ToolCapable: true})
		seen[existing] = true
	}
	slices.SortFunc(catalog, func(a, b modelChoice) int { return strings.Compare(a.ID, b.ID) })
	suggestions := 0
	for _, m := range catalog {
		if !m.ToolCapable || seen[m.ID] {
			continue
		}
		out = append(out, m)
		seen[m.ID] = true
		suggestions++
		if suggestions == 4 {
			break
		}
	}
	return out
}

func resolveSetupModel(selector string, aliases map[string]string) (string, bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", false
	}
	if resolved, known := app.ResolveModelSelector(selector, aliases); known {
		return resolved, resolved != ""
	}
	return "", false
}

//nolint:errcheck // Commit reports are best-effort once persistence has returned.
func reportCommit(out io.Writer, operation, provider string, state authfile.CommitState, err error) {
	fmt.Fprintf(out, "%s %s: %s\n", safeDisplay(operation), safeDisplay(provider), state)
	if err != nil {
		fmt.Fprintln(out, "Operation stopped. Resolve the filesystem condition, then run `mecatui llm status` to inspect passive state.")
	}
}
func commitSucceeded(s authfile.CommitState, err error) bool {
	return err == nil && (s == authfile.CommitNoop || s == authfile.CommitDurable)
}

func commitCredentialThenDefaults(ctx context.Context, authPath, settings, provider, key string, next *defaultSelection, out io.Writer, deps setupDeps) error {
	state, err := deps.updateKey(ctx, authPath, authfile.APIKeyUpdate{Provider: provider, APIKey: &key})
	reportCommit(out, "credential", provider, state, err)
	if !commitSucceeded(state, err) {
		if err == nil {
			err = errors.New("credential commit did not reach a safe state")
		}
		return err
	}
	if next == nil {
		return nil
	}
	state, err = deps.updateDefaults(ctx, settings, *next)
	reportCommit(out, "defaults", provider, state, err)
	if !commitSucceeded(state, err) {
		if err == nil {
			err = errors.New("defaults commit did not reach a safe state")
		}
		return err
	}
	return nil
}

//nolint:errcheck // Post-commit clarification cannot change the persisted outcome.
func removeCredential(ctx context.Context, authPath, settings, provider string, replacement *defaultSelection, out io.Writer, deps setupDeps) error {
	if _, err := fmt.Fprintln(out, "Removing a saved key does not revoke it at the provider. An environment credential may remain active."); err != nil {
		return err
	}
	if replacement != nil {
		state, err := deps.updateDefaults(ctx, settings, *replacement)
		reportCommit(out, "replacement default", replacement.Provider, state, err)
		if err != nil || state != authfile.CommitDurable {
			if err == nil {
				err = errors.New("replacement default was not durably committed")
			}
			return err
		}
	}
	state, err := deps.updateKey(ctx, authPath, authfile.APIKeyUpdate{Provider: provider})
	reportCommit(out, "key removal", provider, state, err)
	if !commitSucceeded(state, err) {
		if replacement != nil {
			if state == authfile.CommitReplacementAppliedDurabilityUnknown {
				fmt.Fprintln(out, "The replacement default is durable; key removal may have applied.")
			} else {
				fmt.Fprintln(out, "The default moved; the old key may remain.")
			}
		}
		if err == nil {
			err = errors.New("key removal did not reach a safe state")
		}
		return err
	}
	return nil
}

type setupRunner struct {
	inFD, outFD            int
	out                    io.Writer
	deps                   setupDeps
	readLine               func(string) (string, error)
	snapshot               setupSnapshot
	authPath, settingsPath string
	explicit               bool
}

func (r *setupRunner) ask(prompt string) (string, error) {
	if _, err := fmt.Fprint(r.out, prompt); err != nil {
		return "", err
	}
	return r.readLine(prompt)
}
func affirmative(v string) bool {
	return strings.EqualFold(strings.TrimSpace(v), "y") || strings.EqualFold(strings.TrimSpace(v), "yes")
}

func (r *setupRunner) run(ctx context.Context) error {
	if r.deps.isTerminal == nil {
		r.deps.isTerminal = term.IsTerminal
	}
	if !r.deps.isTerminal(r.inFD) || !r.deps.isTerminal(r.outFD) {
		return errors.New("llm setup requires terminal stdin and stdout")
	}
	if _, err := fmt.Fprintf(r.out, "%s; %s\n%s\n", providerLabel(openAIProviderID), providerLabel(codexProviderID), codexSetupGuidance); err != nil {
		return err
	}
	for {
		if _, err := fmt.Fprintln(r.out, "\nLLM setup\n  1) Add or replace provider\n  2) Choose default\n  3) Remove saved key\n  4) Exit"); err != nil {
			return err
		}
		choice, err := r.ask("Choice: ")
		if err != nil {
			return fmt.Errorf("read setup choice: %w", err)
		}
		switch strings.TrimSpace(choice) {
		case "1":
			if err := r.add(ctx); err != nil {
				if errors.Is(err, errSetupStarted) {
					return nil
				}
				return err
			}
		case "2":
			if err := r.chooseDefault(ctx); err != nil {
				if errors.Is(err, errSetupStarted) {
					return nil
				}
				return err
			}
		case "3":
			if err := r.remove(ctx); err != nil {
				return err
			}
		case "", "4", "exit", "q":
			return nil
		default:
			return errors.New("invalid setup choice")
		}
	}
}

//nolint:gocyclo,errcheck // The menu keeps custody and lifecycle handoffs explicit.
func (r *setupRunner) add(ctx context.Context) error {
	ids := mutableProviderIDs(r.snapshot)
	fmt.Fprintf(r.out, "API-key providers: %s\n", strings.Join(ids, ", "))
	if len(r.snapshot.NativeIDs) > 0 {
		fmt.Fprintf(r.out, "Native login handoff: %s\n", strings.Join(r.snapshot.NativeIDs, ", "))
	}
	fmt.Fprintln(r.out, "ToolHive credentials: use `thv llm` tooling")
	id, err := r.ask("Provider ID (blank cancels): ")
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if id == codexProviderID {
		_, err := fmt.Fprintln(r.out, codexSetupGuidance)
		return err
	}
	if slices.Contains(r.snapshot.NativeIDs, id) {
		answer, e := r.ask("Continue with native in-process login? [y/N]: ")
		if e != nil {
			return e
		}
		return handoffNative(ctx, id, r.explicit, affirmative(answer), r.out, r.deps)
	}
	if id == "toolhive" {
		fmt.Fprintln(r.out, "ToolHive owns this lifecycle; use `thv llm` tooling.")
		return nil
	}
	if !slices.Contains(ids, id) {
		return errors.New("provider is not eligible for API-key mutation")
	}
	row := providerStatus{}
	for _, candidate := range r.snapshot.Rows {
		if candidate.ID == id {
			row = candidate
		}
	}
	if row.CredentialSource != credentialSourceMissing && row.CredentialSource != credentialSourceAuthFile {
		if !row.FilePresent {
			fmt.Fprintf(r.out, "Existing credential source %s will be reused; no key is copied or written.\n", safeDisplay(row.CredentialSource))
			reportCommit(r.out, "credential reuse", id, authfile.CommitNoop, nil)
			return nil
		}
		replace, readErr := r.ask("An environment credential is active and shadows a saved key. Replace the shadowed file key anyway? [y/N]: ")
		if readErr != nil {
			return readErr
		}
		if !affirmative(replace) {
			reportCommit(r.out, "credential reuse", id, authfile.CommitNoop, nil)
			return nil
		}
		fmt.Fprintln(r.out, "The active environment credential will continue to win; no credential value will be displayed.")
	}
	console := map[string]string{openAIProviderID: "https://platform.openai.com/api-keys", "anthropic": "https://console.anthropic.com/settings/keys", "openrouter": "https://openrouter.ai/settings/keys", openCodeProviderID: "https://opencode.ai/auth"}[id]
	if console == "" {
		console = "the configured provider console"
	}
	if id == openCodeProviderID {
		fmt.Fprintf(r.out, "Open the provider console yourself: %s\nEnter your OpenCode API key with an active Go subscription. This built-in uses the Go endpoint, not Zen pay-as-you-go. API usage may incur charges.\nThe key is stored owner-only plaintext; same-UID processes and an enabled agent Shell can read it.\n", console)
	} else {
		fmt.Fprintf(r.out, "Open the provider console yourself: %s\nEnter an API/developer key, not a consumer subscription. API usage may incur charges.\nThe key is stored owner-only plaintext; same-UID processes and an enabled agent Shell can read it.\n", console)
	}
	var keyBytes []byte
	if r.deps.readSecret != nil {
		keyBytes, err = r.deps.readSecret(ctx)
	} else {
		keyBytes, err = r.deps.readPassword(r.inFD)
	}
	fmt.Fprintln(r.out)
	if err != nil {
		return errors.New("hidden API-key read failed")
	}
	if len(keyBytes) > maxSetupAPIKeyBytes {
		return errors.New("API key exceeds the accepted 8 KiB limit")
	}
	if !utf8.Valid(keyBytes) {
		return errors.New("API key is not valid UTF-8")
	}
	key := string(keyBytes)
	if key == "" {
		return errors.New("API key must not be empty")
	}
	confirm, err := r.ask("Save this credential? [y/N]: ")
	if err != nil {
		return err
	}
	if !affirmative(confirm) {
		return nil
	}
	var next *defaultSelection
	setDefault, err := r.ask("Also choose this provider as the default? [y/N]: ")
	if err != nil {
		return err
	}
	if affirmative(setDefault) {
		d, e := r.promptDefault(id)
		if e != nil {
			return e
		}
		next = &d
		confirmDefault, e := r.ask("Commit this default separately? [y/N]: ")
		if e != nil {
			return e
		}
		if !affirmative(confirmDefault) {
			next = nil
		}
	}
	if err := commitCredentialThenDefaults(ctx, r.authPath, r.settingsPath, id, key, next, r.out, r.deps); err != nil {
		return err
	}
	r.setFileCredential(id, true)
	if next != nil {
		r.snapshot.Default = *next
	}
	return r.maybeStart()
}

func (r *setupRunner) setFileCredential(id string, present bool) {
	for i := range r.snapshot.Rows {
		row := &r.snapshot.Rows[i]
		if row.ID != id {
			continue
		}
		row.FilePresent = present
		if row.CredentialSource == credentialSourceMissing || row.CredentialSource == credentialSourceAuthFile {
			if present {
				row.CredentialSource = credentialSourceAuthFile
			} else {
				row.CredentialSource = credentialSourceMissing
			}
		}
	}
}

func (r *setupRunner) promptDefault(id string) (defaultSelection, error) {
	existing := r.snapshot.ProviderDefaults[id]
	if existing == "" {
		existing = app.BuiltinDefaultModel(id)
	}
	if r.snapshot.Default.Provider == id && r.snapshot.Default.Model != "" {
		existing = r.snapshot.Default.Model
	}
	if id == codexProviderID {
		if _, err := fmt.Fprintln(r.out, "Codex has no static default or offline entitlement inventory. Enter a model manually; normal deployment-default validation is not proof of subscription entitlement."); err != nil {
			return defaultSelection{}, err
		}
	}
	choices := setupModelChoices(existing, r.snapshot.Models[id])
	for _, m := range choices {
		if _, err := fmt.Fprintf(r.out, "  %s — %s\n", safeDisplay(m.ID), safeDisplay(m.Name)); err != nil {
			return defaultSelection{}, err
		}
	}
	if _, err := fmt.Fprintln(r.out, "  manual — enter a model selector (authentication is unverified; the existing deployment-default validator still requires a catalogued model, this provider's declared default, or an alias resolving to one)"); err != nil {
		return defaultSelection{}, err
	}
	sel, err := r.ask("Model selector: ")
	if err != nil {
		return defaultSelection{}, err
	}
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return defaultSelection{}, errors.New("model selector must not be empty")
	}
	resolved, known := resolveSetupModel(sel, r.snapshot.Aliases)
	if !known {
		// Bare selectors may still be the provider's declared default or an
		// authoritative catalog entry; the deployment-default gate decides that.
		resolved = sel
	}
	if resolved == "" {
		return defaultSelection{}, errors.New("model selector is unknown; manual selectors must be catalogued for this provider, match its declared default, or be a configured alias resolving to one of those")
	}
	if err := app.ValidateDeploymentDefaultModel(id, resolved, r.snapshot.ProviderDefaults[id]); err != nil {
		return defaultSelection{}, fmt.Errorf("manual model cannot be saved as the deployment default: %w", err)
	}
	return defaultSelection{Provider: id, Model: resolved}, nil
}
func defaultProviderIDs(s setupSnapshot) []string {
	var ids []string
	for _, row := range s.Rows {
		if row.Kind == "none required" || row.LocalCredential == credentialLocallyUsable || (row.Mutable && row.CredentialSource != credentialSourceMissing) {
			ids = append(ids, row.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func defaultProviderIDsAfterRemoval(s setupSnapshot, removed string) []string {
	var ids []string
	for _, row := range s.Rows {
		if row.Kind == "none required" || row.LocalCredential == credentialLocallyUsable {
			ids = append(ids, row.ID)
			continue
		}
		if !row.Mutable || row.CredentialSource == credentialSourceMissing {
			continue
		}
		if row.ID == removed && row.CredentialSource == credentialSourceAuthFile {
			continue
		}
		ids = append(ids, row.ID)
	}
	slices.Sort(ids)
	return ids
}

//nolint:gocyclo // Default selection keeps local credential and native-consent gates together.
func (r *setupRunner) chooseDefault(ctx context.Context) error {
	ids := defaultProviderIDs(r.snapshot)
	if len(ids) == 0 && len(r.snapshot.NativeIDs) == 0 {
		return errors.New("no provider has a usable credential or a configured no-auth definition; add or enroll one first")
	}
	labels := make([]string, len(ids))
	for i, id := range ids {
		labels[i] = providerLabel(id)
	}
	if _, err := fmt.Fprintf(r.out, "Available default providers: %s\n", strings.Join(labels, ", ")); err != nil {
		return err
	}
	if len(r.snapshot.NativeIDs) > 0 {
		if _, err := fmt.Fprintf(r.out, "Native defaults require an explicit local enrollment check: %s\n", strings.Join(r.snapshot.NativeIDs, ", ")); err != nil {
			return err
		}
	}
	id, err := r.ask("Provider ID (blank cancels): ")
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if slices.Contains(r.snapshot.NativeIDs, id) {
		consent, consentErr := r.ask("Inspect this native endpoint's encrypted local enrollment status? [y/N]: ")
		if consentErr != nil {
			return consentErr
		}
		if !affirmative(consent) {
			return nil
		}
		if r.deps.nativeUsable == nil {
			return errors.New("native enrollment status is unavailable; run endpoint-specific `mecatui llm status` first")
		}
		usable, statusErr := r.deps.nativeUsable(ctx, id)
		if statusErr != nil || !usable {
			return errors.New("native endpoint is not locally usable; run endpoint-specific login/status before selecting it as the default")
		}
		ids = append(ids, id)
	}
	if !slices.Contains(ids, id) {
		return errors.New("provider is unknown or has no usable credential; add or enroll it before selecting it as the deployment default")
	}
	d, err := r.promptDefault(id)
	if err != nil {
		return err
	}
	yes, err := r.ask("Commit this default? [y/N]: ")
	if err != nil {
		return err
	}
	if !affirmative(yes) {
		return nil
	}
	state, err := r.deps.updateDefaults(ctx, r.settingsPath, d)
	reportCommit(r.out, "defaults", id, state, err)
	if !commitSucceeded(state, err) {
		if err == nil {
			err = errors.New("defaults not safely committed")
		}
		return err
	}
	r.snapshot.Default = d
	return r.maybeStart()
}

//nolint:gocyclo // Removal separates read-only Codex guidance from ordered key/default commits.
func (r *setupRunner) remove(ctx context.Context) error {
	id, err := r.ask("Provider ID to remove (blank cancels): ")
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if id == codexProviderID {
		_, err := fmt.Fprintln(r.out, codexSetupGuidance)
		return err
	}
	if !slices.Contains(mutableProviderIDs(r.snapshot), id) {
		return errors.New("provider is not eligible for API-key mutation")
	}
	yes, err := r.ask("Remove the saved key? This does not revoke it. [y/N]: ")
	if err != nil {
		return err
	}
	if !affirmative(yes) {
		return nil
	}
	remainingActive := false
	for _, row := range r.snapshot.Rows {
		if row.ID == id && row.CredentialSource != credentialSourceAuthFile && row.CredentialSource != credentialSourceMissing {
			remainingActive = true
			if _, err := fmt.Fprintf(r.out, "Remaining active credential source after file removal: %s\n", safeDisplay(row.CredentialSource)); err != nil {
				return err
			}
		}
	}
	var replacement *defaultSelection
	if r.snapshot.Default.Provider == id && !remainingActive {
		rid, e := r.ask("Replacement default provider: ")
		if e != nil {
			return e
		}
		rid = strings.TrimSpace(rid)
		if !slices.Contains(defaultProviderIDsAfterRemoval(r.snapshot, id), rid) {
			return errors.New("replacement provider is unknown or has no usable credential; add or enroll it before removing the active default key")
		}
		d, e := r.promptDefault(rid)
		if e != nil {
			return e
		}
		confirm, e := r.ask("Commit the replacement default before removing the key? [y/N]: ")
		if e != nil {
			return e
		}
		if !affirmative(confirm) {
			return nil
		}
		replacement = &d
	}
	if err := removeCredential(ctx, r.authPath, r.settingsPath, id, replacement, r.out, r.deps); err != nil {
		return err
	}
	r.setFileCredential(id, false)
	if replacement != nil {
		r.snapshot.Default = *replacement
	}
	return nil
}
func (r *setupRunner) maybeStart() error {
	answer, err := r.ask("Start mecatui now? [y/N]: ")
	if err != nil {
		return err
	}
	if !affirmative(answer) {
		return nil
	}
	if r.deps.start == nil {
		return errors.New("startup unavailable")
	}
	if err := r.deps.start(r.authPath); err != nil {
		return err
	}
	return errSetupStarted
}

func preflightSetupPaths(authPath, settings string) error {
	auth, err := canonicalSetupTarget(authPath, filepath.Clean(authPath) == filepath.Clean(authfile.DefaultPath(xdgconfig.OSEnv)))
	if err != nil {
		return fmt.Errorf("resolve auth target: %w", err)
	}
	settingsTarget, err := canonicalSetupTarget(settings, true)
	if err != nil {
		return fmt.Errorf("resolve settings target: %w", err)
	}
	if auth == settingsTarget {
		return errors.New("auth and settings targets resolve to the same file")
	}
	// #nosec G703 -- both paths were canonicalized through existing physical parents.
	authInfo, authErr := os.Stat(auth)
	// #nosec G703 -- both paths were canonicalized through existing physical parents.
	settingsInfo, settingsErr := os.Stat(settingsTarget)
	if authErr == nil && settingsErr == nil && os.SameFile(authInfo, settingsInfo) {
		return errors.New("auth and settings targets resolve to the same file")
	}
	if authErr != nil && !errors.Is(authErr, os.ErrNotExist) {
		return errors.New("auth target is unavailable")
	}
	if settingsErr != nil && !errors.Is(settingsErr, os.ErrNotExist) {
		return errors.New("settings target is unavailable")
	}
	return nil
}

func preflightSetupDocuments(authPath, settings string) error {
	if err := preflightSetupPaths(authPath, settings); err != nil {
		return err
	}
	if err := authfile.PreflightAPIKeyUpdateTarget(authPath); err != nil {
		return fmt.Errorf("validate auth target: %w", err)
	}
	if err := permconfig.PreflightDefaultsUpdateTarget(settings); err != nil {
		return fmt.Errorf("validate settings target: %w", err)
	}
	return nil
}

func canonicalSetupTarget(path string, allowMissingManagedParent bool) (string, error) {
	target, err := authfile.CanonicalPath(path)
	if err == nil || !allowMissingManagedParent {
		return target, err
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		return "", errors.New("path is unavailable")
	}
	parent := filepath.Dir(abs)
	base, baseErr := filepath.EvalSymlinks(filepath.Dir(parent))
	if baseErr != nil {
		return "", errors.New("path parent is unavailable")
	}
	if filepath.Base(parent) != "mecatl" {
		return "", errors.New("path parent is unavailable")
	}
	return filepath.Join(base, "mecatl", filepath.Base(abs)), nil
}

func readLineContext(ctx context.Context, in *os.File, reader *bufio.Reader) (string, error) {
	result := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := reader.ReadString('\n')
		result <- struct {
			value string
			err   error
		}{value, err}
	}()
	select {
	case got := <-result:
		return got.value, got.err
	case <-ctx.Done():
		_ = in.Close()
		<-result
		return "", ctx.Err()
	}
}

func readPasswordContext(ctx context.Context, in *os.File) ([]byte, error) {
	fd := int(in.Fd())
	state, stateErr := term.GetState(fd)
	result := make(chan struct {
		value []byte
		err   error
	}, 1)
	go func() {
		value, err := term.ReadPassword(fd)
		result <- struct {
			value []byte
			err   error
		}{value, err}
	}()
	select {
	case got := <-result:
		return got.value, got.err
	case <-ctx.Done():
		if stateErr == nil {
			_ = term.Restore(fd, state)
		}
		_ = in.Close()
		<-result
		return nil, ctx.Err()
	}
}

func runSetupCommand(ctx context.Context, path string, explicit bool, in, out *os.File, deps setupDeps) error {
	deps = withSetupDefaults(deps)
	if !deps.writeSupported() {
		return errors.New("llm setup credential/default writes are unsupported on this platform; configure settings.yaml and auth.yaml manually with owner-only permissions")
	}
	if !deps.isTerminal(int(in.Fd())) || !deps.isTerminal(int(out.Fd())) {
		return errors.New("llm setup requires terminal stdin and stdout")
	}
	settings := settingsPath(xdgconfig.OSEnv)
	if err := deps.preflight(path, settings); err != nil {
		return err
	}
	s, err := deps.load(path, explicit)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	deps.readSecret = func(readCtx context.Context) ([]byte, error) { return readPasswordContext(readCtx, in) }
	readLine := func() (string, error) { return readLineContext(ctx, in, reader) }
	if s.ExplicitMissing {
		if _, e := fmt.Fprintf(out, "The explicit auth file %s is missing. Create that leaf in its existing protected parent? [y/N]: ", safeDisplay(path)); e != nil {
			return e
		}
		answer, e := readLine()
		if e != nil {
			return fmt.Errorf("confirm explicit auth path: %w", e)
		}
		if !affirmative(answer) {
			return nil
		}
	}
	r := setupRunner{inFD: int(in.Fd()), outFD: int(out.Fd()), out: out, deps: deps, snapshot: s, authPath: path, settingsPath: settings, explicit: explicit, readLine: func(string) (string, error) { line, e := readLine(); return strings.TrimSpace(line), e }}
	return r.run(ctx)
}
