//go:build !linux

package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetupProductionCapabilityRejectsBeforeInteraction(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.WriteString("1\nopenai\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if defaultSetupDeps().writeSupported() {
		t.Fatal("production setup advertised unsupported mutation")
	}
	err = runSetupCommand(t.Context(), "auth.yaml", true, in, out, defaultSetupDeps())
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("runSetupCommand = %v", err)
	}
	info, statErr := out.Stat()
	if statErr != nil || info.Size() != 0 {
		t.Fatalf("unsupported setup interacted with terminal: size=%d err=%v", info.Size(), statErr)
	}
}
