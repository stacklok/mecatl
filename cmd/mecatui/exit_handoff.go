package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

const finalSessionHandoffPrefix = "mecatui: final-session-id="

type activeSessionReporter interface {
	ActiveSessionID() string
}

type sessionSnapshotGetter interface {
	GetSession(context.Context, string) (client.SessionSnapshot, error)
}

// finishFinalSessionHandoff reads before the client closes, then prints only after cleanup.
func finishFinalSessionHandoff(w io.Writer, final tea.Model, runErr error, interrupted, embedded bool, getter sessionSnapshotGetter, cleanup func()) {
	var snapshot client.SessionSnapshot
	var available bool
	reporter, ok := final.(activeSessionReporter)
	if embedded && ok && shouldWriteFinalSessionHandoff(final, runErr, interrupted) && reporter.ActiveSessionID() != "" && utf8.ValidString(reporter.ActiveSessionID()) && getter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var err error
		snapshot, err = getter.GetSession(ctx, reporter.ActiveSessionID())
		available = err == nil && ctx.Err() == nil && snapshot.State != ""
		cancel()
	}
	cleanup()
	if !shouldWriteFinalSessionHandoff(final, runErr, interrupted) || !ok {
		return
	}
	id := reporter.ActiveSessionID()
	// Connected mode, and an ID unsafe to print as one terminal line, keep the
	// JSON-quoted record; an embedded safe ID gets the aligned human summary instead.
	if !embedded || id == "" || !utf8.ValidString(id) || !safeHandoffID(id) {
		writeFinalSessionHandoff(w, final)
		return
	}
	var human strings.Builder
	human.WriteByte('\n')
	writeHandoffField(&human, "Session ID:", id)
	if available {
		if title := strings.TrimSpace(terminaltext.SanitizeSingleLine(snapshot.Title)); title != "" {
			writeHandoffField(&human, "Title:", title)
		}
		writeHandoffField(&human, "Model calls:", fmt.Sprint(snapshot.Turns))
		writeHandoffField(&human, "Tokens (main):", handoffTokens(snapshot.Usage))
		if snapshot.AuxiliaryUsage != (client.Usage{}) {
			writeHandoffField(&human, "Tokens (aux):", handoffTokens(snapshot.AuxiliaryUsage))
		}
	}
	writeHandoffField(&human, "Resume:", "mecatui --resume '"+strings.ReplaceAll(id, "'", "'\"'\"'")+"'")
	writeHandoffField(&human, "", "mecatui --resume-latest (may select a different chat)")
	_, _ = io.WriteString(w, human.String())
}

// handoffLabelWidth aligns values after the widest label, "Tokens (main): ".
const handoffLabelWidth = len("Tokens (main): ")

func writeHandoffField(b *strings.Builder, label, value string) {
	_, _ = fmt.Fprintf(b, "%-*s%s\n", handoffLabelWidth, label, value)
}

// handoffTokens humanizes one usage value; cache counts are labelled
// components of input, never summed with it.
func handoffTokens(u client.Usage) string {
	s := renderfmt.HumanizeTokens(u.InputTokens) + " input, " + renderfmt.HumanizeTokens(u.OutputTokens) + " output"
	if u.CacheReadTokens != 0 {
		s += ", " + renderfmt.HumanizeTokens(u.CacheReadTokens) + " cache read"
	}
	if u.CacheWriteTokens != 0 {
		s += ", " + renderfmt.HumanizeTokens(u.CacheWriteTokens) + " cache write"
	}
	return s
}

func safeHandoffID(id string) bool {
	for _, r := range id {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func maybeWriteFinalSessionHandoff(w io.Writer, final tea.Model, runErr error, interrupted bool) bool {
	if runErr != nil || interrupted {
		return false
	}
	return writeFinalSessionHandoff(w, final)
}

func writeFinalSessionHandoff(w io.Writer, final tea.Model) bool {
	reporter, ok := final.(activeSessionReporter)
	if !ok {
		return false
	}
	id := reporter.ActiveSessionID()
	if id == "" || !utf8.ValidString(id) {
		return false
	}
	quoted, err := json.Marshal(id)
	if err != nil {
		return false
	}
	_, err = io.WriteString(w, "\n"+finalSessionHandoffPrefix+string(quoted)+"\n")
	return err == nil
}
