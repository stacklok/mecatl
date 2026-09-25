package main

import (
	"strings"
	"testing"
)

func TestMecatuiCompactToolCards_Scenario2_StrictClientSetting(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		if got := mustReadClientSettings(t).ToolCards.CollapsedResultRows; got != 3 {
			t.Fatalf("collapsed result rows = %d, want 3", got)
		}
	})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{name: "missing section", body: "terminal_title:\n  enabled: true\n", want: 3},
		{name: "missing key", body: "tool_cards: {}\n", want: 3},
		{name: "positive value", body: "tool_cards:\n  collapsed_result_rows: 7\n", want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeSettings(t, "mecatui", tc.body)
			if got := mustReadClientSettings(t).ToolCards.CollapsedResultRows; got != tc.want {
				t.Fatalf("collapsed result rows = %d, want %d", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "zero", body: "tool_cards:\n  collapsed_result_rows: 0\n"},
		{name: "negative", body: "tool_cards:\n  collapsed_result_rows: -2\n"},
		{name: "wrong type", body: "tool_cards:\n  collapsed_result_rows: SECRET-WRONG-TYPE\n"},
		{name: "unknown nested key", body: "tool_cards:\n  collapsed_result_rowz: 9\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeSettings(t, "mecatui", tc.body)
			_, err := readClientSettings()
			if err == nil {
				t.Fatal("invalid tool card setting was accepted")
			}
			for _, want := range []string{"tool_cards", "collapsed_result_rows"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want field guidance containing %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "SECRET-WRONG-TYPE") {
				t.Fatalf("configuration error leaked field value: %q", err)
			}
		})
	}
}
