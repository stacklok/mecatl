package main

import (
	"encoding/json"
	"io"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

const finalSessionHandoffPrefix = "mecatui: final-session-id="

type activeSessionReporter interface {
	ActiveSessionID() string
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
	_, err = io.WriteString(w, finalSessionHandoffPrefix+string(quoted)+"\n")
	return err == nil
}
