package adapter

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAttemptRepositoryProtocolHasOnlyLifecycleOperations(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating attempt repository protocol")
	}
	protocolPath := filepath.Join(filepath.Dir(file), "..", "..", "contracts", "proto", "mecatl", "driver", "v1", "attempt_repository.proto")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(protocol), "ADR-0250 watches session events only") {
		t.Fatal("attempt repository protocol does not document that ADR-0250 watches session events only")
	}
	wantRPCs := []string{
		"CreateAttempt", "GetAttempt", "ListAttempts", "DiscoverAttemptWork", "AcquireAttemptClaim", "RenewAttemptClaim", "CheckpointAttempt",
		"ReleaseAttemptClaim", "FinalizeAttempt", "RetryAttempt", "AbandonAttempt", "DeleteAttempt", "DeleteTerminalAttemptsOlderThan",
	}
	var gotRPCs []string
	for _, line := range strings.Split(string(protocol), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "rpc ") {
			gotRPCs = append(gotRPCs, strings.Fields(line)[1][:strings.IndexByte(strings.Fields(line)[1], '(')])
		}
	}
	if !reflect.DeepEqual(gotRPCs, wantRPCs) {
		t.Fatalf("attempt repository RPCs = %v, want lifecycle operations only %v", gotRPCs, wantRPCs)
	}
}
