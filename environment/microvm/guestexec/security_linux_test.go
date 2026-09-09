//go:build linux

package guestexec

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestInvariant_guest_workload_is_unprivileged(t *testing.T) {
	identity := DefaultWorkloadIdentity()
	if identity.UID == 0 || identity.GID == 0 {
		t.Fatalf("production workload identity = %d:%d, must not be guest root", identity.UID, identity.GID)
	}

	cmd := exec.Command("/bin/sh", "-c", "true")
	configureProcessGroup(cmd, defaultCancelGrace)
	configureWorkloadIdentity(cmd, identity)
	attr := cmd.SysProcAttr
	if attr == nil || attr.Credential == nil {
		t.Fatal("guest workload has no kernel credential drop")
	}
	if attr.Credential.Uid != identity.UID || attr.Credential.Gid != identity.GID || !attr.Credential.NoSetGroups {
		t.Fatalf("guest workload credential = %#v, want dedicated %d:%d with no supplementary groups", attr.Credential, identity.UID, identity.GID)
	}
	if len(attr.AmbientCaps) != 0 {
		t.Fatalf("guest workload ambient capabilities = %v, want none", attr.AmbientCaps)
	}
	if attr.Setpgid != true {
		t.Fatal("guest workload lost process-group cancellation")
	}
}

func TestGuestServerRejectsRootWorkloadIdentity(t *testing.T) {
	_, err := NewGuestServer(ServerConfig{Binding: scenarioBinding, WorkspaceRoot: t.TempDir(), WorkloadIdentity: WorkloadIdentity{UID: 0, GID: 0}})
	if err == nil {
		t.Fatal("guest exec accepted root workload identity")
	}
}

func TestGuestWorkloadIdentityAllowsNormalWorkspaceCommand(t *testing.T) {
	identity := WorkloadIdentity{UID: uint32(syscall.Geteuid()), GID: uint32(syscall.Getegid())}
	server, err := NewGuestServer(ServerConfig{Binding: scenarioBinding, WorkspaceRoot: t.TempDir(), WorkloadIdentity: identity})
	if err != nil {
		t.Fatalf("NewGuestServer with unprivileged test identity: %v", err)
	}
	if server.identity != identity {
		t.Fatalf("server identity = %#v, want %#v", server.identity, identity)
	}
}
