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
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
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
	if !shouldWriteFinalSessionHandoff(final, runErr, interrupted) || !writeFinalSessionHandoff(w, final) || !embedded || !ok {
		return
	}
	id := reporter.ActiveSessionID()
	var human strings.Builder
	if available {
		if title := strings.TrimSpace(terminaltext.SanitizeSingleLine(snapshot.Title)); title != "" {
			_, _ = fmt.Fprintf(&human, "Session: %s\n", title)
		}
		_, _ = fmt.Fprintf(&human, "Model calls: %d\n", snapshot.Turns)
		writeHandoffTokens(&human, "main", snapshot.Usage)
		if snapshot.AuxiliaryUsage != (client.Usage{}) {
			writeHandoffTokens(&human, "aux", snapshot.AuxiliaryUsage)
		}
	}
	if safeHandoffID(id) {
		_, _ = fmt.Fprintf(&human, "Resume: mecatui --resume '%s'\n", strings.ReplaceAll(id, "'", "'\"'\"'"))
		human.WriteString("Or: mecatui --resume-latest (may select a different chat)\n")
	}
	_, _ = io.WriteString(w, human.String())
}

// writeHandoffTokens writes one humanized usage line; cache counts are labelled
// components of input, never summed with it.
func writeHandoffTokens(b *strings.Builder, label string, u client.Usage) {
	_, _ = fmt.Fprintf(b, "Tokens (%s): %s input, %s output", label, ui.HumanizeTokens(u.InputTokens), ui.HumanizeTokens(u.OutputTokens))
	if u.CacheReadTokens != 0 {
		_, _ = fmt.Fprintf(b, ", %s cache read", ui.HumanizeTokens(u.CacheReadTokens))
	}
	if u.CacheWriteTokens != 0 {
		_, _ = fmt.Fprintf(b, ", %s cache write", ui.HumanizeTokens(u.CacheWriteTokens))
	}
	b.WriteByte('\n')
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
