//go:build linux

package guestexec

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestInvariant_guest_workload_is_unprivileged(t *testing.T) {
	defaultIdentity := DefaultWorkloadIdentity()
	if defaultIdentity.UID == 0 || defaultIdentity.GID == 0 {
		t.Fatalf("production workload identity = %d:%d, must not be guest root", defaultIdentity.UID, defaultIdentity.GID)
	}

	identity := WorkloadIdentity{UID: uint32(os.Getuid()) ^ 1, GID: uint32(os.Getgid()) ^ 1}
	cmd := exec.Command("/bin/sh", "-c", "true")
	configureProcessGroup(cmd, defaultCancelGrace)
	configureWorkloadIdentity(cmd, identity)
	attr := cmd.SysProcAttr
	if attr == nil || attr.Credential == nil {
		t.Fatal("different guest workload identity has no kernel credential drop")
	}
	if attr.Credential.Uid != identity.UID || attr.Credential.Gid != identity.GID || attr.Credential.NoSetGroups || attr.Credential.Groups == nil || len(attr.Credential.Groups) != 0 {
		t.Fatalf("guest workload credential = %#v, want dedicated %d:%d with an explicit empty supplementary-group set", attr.Credential, identity.UID, identity.GID)
	}
	if len(attr.AmbientCaps) != 0 {
		t.Fatalf("guest workload ambient capabilities = %v, want none", attr.AmbientCaps)
	}
	if attr.Setpgid != true {
		t.Fatal("guest workload lost process-group cancellation")
	}
}

func TestConfigureWorkloadIdentitySameIdentitySkipsCredential(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "true")
	configureWorkloadIdentity(cmd, WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())})

	if cmd.SysProcAttr == nil {
		t.Fatal("same-identity workload has no process attributes")
	}
	if cmd.SysProcAttr.Credential != nil {
		t.Fatalf("same-identity workload credential = %#v, want nil", cmd.SysProcAttr.Credential)
	}
	if len(cmd.SysProcAttr.AmbientCaps) != 0 {
		t.Fatalf("same-identity workload ambient capabilities = %v, want none", cmd.SysProcAttr.AmbientCaps)
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
