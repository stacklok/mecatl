package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/stacklok/toolhive-core/audit"
)

// auditLine is one audit event as the toolhive-core logger wrote it, with
// the fields the tests read.
type auditLine struct {
	AuditID   string            `json:"audit_id"`
	Type      string            `json:"type"`
	Outcome   string            `json:"outcome"`
	Component string            `json:"component"`
	Subjects  map[string]string `json:"subjects"`
	Data      struct {
		Lineage struct {
			ParentRecord string `json:"parent_record"`
		} `json:"lineage"`
	} `json:"data"`
	Delegation json.RawMessage `json:"delegation"`
}

// readAuditFile returns the events in the audit file path, oldest first.
func readAuditFile(t *testing.T, path string) []auditLine {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the audit file: %v", err)
	}
	var out []auditLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev auditLine
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("audit line %q is not JSON: %v", sc.Text(), err)
		}
		out = append(out, ev)
	}
	return out
}

// delegationChainJSON returns the delegation chain toolhive-core logs for a
// token's act claim, as its JSON slog output writes it.
func delegationChainJSON(t *testing.T, act any) string {
	t.Helper()
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "delegation", audit.ParseDelegationChain(act, 0))
	var line struct {
		Delegation json.RawMessage `json:"delegation"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	return string(line.Delegation)
}
