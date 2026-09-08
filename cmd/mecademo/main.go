package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/provider/openai"
)

func main() {
	if buildinfo.IsVersion(os.Args) {
		buildinfo.PrintVersion(os.Stdout, "mecademo")
		return
	}
	parsed, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "mecademo:", err)
		os.Exit(2)
	}

	provider, label, err := selectProvider(parsed.useOpenAI, parsed.model, parsed.baseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mecademo:", err)
		os.Exit(1)
	}

	fmt.Printf("=== mecatl demo (%s) ===\n", label)
	fmt.Println("Driving a real agent.Engine: auto-allowed tool call -> permission ask + approval -> final result.")
	// The demo never configures a guardrails checker (no --guardrails-model, no
	// `guardrail` slot), so the LLM-backed content checker is OFF — stated explicitly,
	// mirroring the composition build-once posture line (logGuardrailsPosture).
	fmt.Println("guardrails: OFF (no checker model configured; bind the `guardrail` model slot or set --guardrails-model to enable)")
	fmt.Println()

	events, err := RunScenario(context.Background(), provider, parsed.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mecademo:", err)
		os.Exit(1)
	}

	for _, ev := range events {
		fmt.Println(formatEvent(ev))
	}

	// Second act (offline only): a 2-member agent team whose lead consolidates the
	// worker's recorded finding into a single report — the team's deliverable.
	if !parsed.useOpenAI {
		fmt.Println()
		fmt.Println("=== mecatl team demo (offline) ===")
		fmt.Println("A lead + worker coordinate; the worker records a finding; the lead synthesises the consolidated report.")
		fmt.Println()
		outcome, terr := RunTeamScenario(context.Background())
		if terr != nil {
			fmt.Fprintln(os.Stderr, "mecademo team:", terr)
			os.Exit(1)
		}
		fmt.Printf("team finished in %d round(s); quiescent=%t\n", outcome.Rounds, outcome.Quiescent)
		fmt.Println("--- consolidated report (the team's deliverable) ---")
		fmt.Println(outcome.Report)

		// Third act: a background subagent — immediate started-result, the
		// harness completion NOTICE injected at the next turn boundary (recorded
		// history, not an event), and the SubagentStatus collection of the body.
		fmt.Println()
		fmt.Println("=== mecatl background subagent demo (offline) ===")
		fmt.Println("A subagent runs in the background; the harness notice lands at the next turn boundary; SubagentStatus collects the result.")
		fmt.Println()
		bgEvents, notes := RunBackgroundScenario(context.Background())
		for _, ev := range bgEvents {
			fmt.Println(formatEvent(ev))
		}
		fmt.Println("--- harness notice(s) injected into the model's history at the turn boundary ---")
		for _, n := range notes {
			fmt.Println(n)
		}
	}
}

type demoFlags struct {
	useOpenAI bool
	model     string
	baseURL   string
}

func parseFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("mecademo", flag.ContinueOnError)
	var parsed demoFlags
	fs.BoolVar(&parsed.useOpenAI, "openai", false, "run against the live OpenAI Responses API (key from OPENAI_API_KEY)")
	fs.StringVar(&parsed.model, "model", demoModel, "model identifier when --openai is set")
	fs.StringVar(&parsed.baseURL, "openai-base-url", "", "override the OpenAI API base URL")
	fs.Usage = func() {
		cliconfig.PrintDefaults(fs.Output(), fs)
		_, _ = fmt.Fprintln(fs.Output(), "\nVersion: mecademo --version prints the build version and exits.")
	}
	if err := fs.Parse(args); err != nil {
		return demoFlags{}, err
	}
	return parsed, nil
}

// selectProvider returns the configured LLMProvider and a human label. The
// offline mock is the default; --openai (with OPENAI_API_KEY) selects the live
// Responses adapter so the same scenario runs against a real model.
func selectProvider(useOpenAI bool, model, baseURL string) (port.LLMProvider, string, error) {
	if !useOpenAI {
		return mockProvider(), "offline / mockllm", nil
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, "", fmt.Errorf("--openai requires OPENAI_API_KEY to be set")
	}
	opts := []openai.Option{openai.WithAPIKey(key)}
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	return openai.New(opts...), "live / openai " + model, nil
}

// formatEvent renders one streamed Event as a single readable line so a human
// watching the terminal sees the loop, the tool call, the permission
// prompt+approval, and the final result + usage.
func formatEvent(ev session.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%03d] turn=%d %-14s", ev.Seq, ev.Turn, ev.Type)

	switch ev.Type {
	case session.EvMessageDelta:
		fmt.Fprintf(&b, " text=%q", ev.Text)
	case session.EvToolCall:
		if ev.ToolCall != nil {
			fmt.Fprintf(&b, " tool=%s args=%s", ev.ToolCall.Name, string(ev.ToolCall.Args))
		}
	case session.EvToolResult:
		if ev.ToolResult != nil {
			fmt.Fprintf(&b, " error=%t result=%q", ev.ToolResult.IsError, oneLine(ev.ToolResult.Content))
		}
	case session.EvPermissionAsk:
		if ev.Ask != nil {
			fmt.Fprintf(&b, " ASK tool=%s reason=%q  -> client auto-approves", ev.Ask.Tool, ev.Ask.Reason)
		}
	case session.EvHook:
		fmt.Fprintf(&b, " %s", ev.Text)
	case session.EvSubagentStart:
		if ev.Subagent != nil {
			fmt.Fprintf(&b, " child=%s background=%t goal=%q", ev.Subagent.ChildID, ev.Subagent.Background, ev.Subagent.Goal)
		}
	case session.EvSubagentEnd:
		if ev.Subagent != nil {
			fmt.Fprintf(&b, " child=%s stop=%s", ev.Subagent.ChildID, ev.Subagent.Stop)
			// A failed delegation's WHY (session.SubagentPayload.Cause) — printed only
			// when set, so the clean-run output is unchanged. mecademo is the runnable
			// example a library consumer copies to learn the event taxonomy, so a field
			// that appears in no example is a field they never discover. The field is
			// already clamped and whitespace-collapsed at the emit site, so a consumer
			// just renders it.
			if ev.Subagent.Cause != "" {
				fmt.Fprintf(&b, " cause=%q", ev.Subagent.Cause)
			}
		}
	case session.EvCompaction:
		fmt.Fprintf(&b, " summary=%q", oneLine(ev.Text))
	case session.EvResult:
		if ev.Result != nil {
			fmt.Fprintf(&b, " stop=%s text=%q", ev.Result.Stop, ev.Result.Text)
			if ev.Result.Error != "" {
				fmt.Fprintf(&b, " error=%q", ev.Result.Error)
			}
			u := ev.Result.Usage
			fmt.Fprintf(&b, "\n      usage: in=%d out=%d cacheRead=%d cacheWrite=%d cacheHitRate=%.2f",
				u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CacheHitRate())
		}
	}
	return b.String()
}

// oneLine collapses newlines so a multi-line tool result prints on a single row.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", " ⏎ ")
}
