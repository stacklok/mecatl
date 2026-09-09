package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/microvmcmd"
)

func TestMicroVMLifecycleUX_StatusAndDeleteReportExactGeneration(t *testing.T) {
	mgr := &fakeMicroVMManager{status: microvmmanager.Status{
		Configured: true, Running: true, Socket: "/run/microvmd.sock",
		Generations: []microvmmanager.Generation{{SessionID: "s1", Ref: "env-1@7", Generation: 7, WorktreePath: "/worktrees/s1", Health: "stale"}},
	}, deleteResult: microvmmanager.DeleteResult{WorktreePath: "/worktrees/s1", WorktreeRetained: true}}
	var out strings.Builder
	if err := runMicroVMCommand(context.Background(), []string{"status"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"session=s1", "ref=env-1@7", "repository-generation=7", "health=stale", "worktree=/worktrees/s1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status omitted %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	args := []string{"delete", "--session", "s1", "--ref", "env-1@7", "--generation", "7", "--yes"}
	if err := runMicroVMCommand(context.Background(), args, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	if mgr.deleteRequest != (microvmmanager.DeleteRequest{SessionID: "s1", Ref: "env-1@7", Generation: 7}) {
		t.Fatalf("delete request = %+v", mgr.deleteRequest)
	}
	if !strings.Contains(out.String(), "dirty worktree retained: /worktrees/s1") {
		t.Fatalf("dirty retention was not reported: %s", out.String())
	}
}

func TestMecatuiMicroVM_RepositoryGenerationShowsDistinctLogicalWorktrees(t *testing.T) {
	mgr := &fakeMicroVMManager{status: microvmmanager.Status{
		Configured: true, Running: true, Continuation: "next",
		Generations: []microvmmanager.Generation{
			{SessionID: "session-a", Ref: "logical-a@4", Generation: 4, WorktreePath: "/worktrees/a", State: "ready", Health: "healthy"},
			{SessionID: "session-b", Ref: "logical-b@4", Generation: 4, WorktreePath: "/worktrees/b", State: "ready", Health: "healthy"},
		},
	}, deleteResult: microvmmanager.DeleteResult{WorktreePath: "/worktrees/b", WorktreeRetained: true}}
	var out strings.Builder
	if err := runMicroVMCommand(t.Context(), []string{"status", "--page-size", "2"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"session=session-a", "session=session-b", "repository-generation=4", "worktree=/worktrees/a", "worktree=/worktrees/b", "--continuation"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("repository status omitted %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := runMicroVMCommand(t.Context(), []string{"delete", "--session", "session-b", "--ref", "logical-b@4", "--generation", "4", "--yes"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "shared repository VM is not deleted") || !strings.Contains(out.String(), "dirty worktree retained") {
		t.Fatalf("logical delete UX is ambiguous:\n%s", out.String())
	}
}

func TestMecatuiMicroVM_StatusOutputIsOneBoundedContinuablePage(t *testing.T) {
	generations := make([]microvmmanager.Generation, 64)
	for i := range generations {
		generations[i] = microvmmanager.Generation{SessionID: fmt.Sprintf("s-%03d", i), Ref: fmt.Sprintf("env-%03d@1", i), Generation: 1, WorktreePath: fmt.Sprintf("/worktrees/%03d", i), Health: "stale"}
	}
	mgr := &fakeMicroVMManager{status: microvmmanager.Status{Configured: true, Running: true, Generations: generations, Continuation: "opaque-next"}}
	var out strings.Builder
	if err := runMicroVMCommand(context.Background(), []string{"status", "--page-size", "64"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "logical worktree: "); got != 64 {
		t.Fatalf("generation rows = %d, want 64", got)
	}
	want := `mecatui microvm status --continuation "opaque-next"`
	if !strings.Contains(out.String(), want) {
		t.Fatalf("status omitted continuation command %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "mecated microvm status --continuation") {
		t.Fatalf("status named uninvoked mecated frontend:\n%s", out.String())
	}
	if mgr.statusRequest.PageSize != 64 || mgr.statusRequest.Continuation != "" {
		t.Fatalf("status request = %+v", mgr.statusRequest)
	}
}

func TestMicroVMFirstRunRepair_DeleteValidatesTargetBeforePrompt(t *testing.T) {
	mgr := &fakeMicroVMManager{status: microvmmanager.Status{Configured: true, Running: true}}
	var out strings.Builder
	err := runMicroVMCommand(context.Background(), []string{"delete", "--session", "missing", "--ref", "env@2", "--generation", "2"}, strings.NewReader("yes\n"), &out, mgr, true)
	if err == nil || !strings.Contains(err.Error(), "not present in microvm status") {
		t.Fatalf("invalid target error = %v", err)
	}
	if strings.Contains(out.String(), "[y/N]") || mgr.deleteCalls != 0 {
		t.Fatalf("invalid target reached confirmation/delete: out=%q calls=%d", out.String(), mgr.deleteCalls)
	}
}

func TestMicroVMDoctorFailureGuidesRetryNotStatus(t *testing.T) {
	mgr := &fakeMicroVMManager{doctorErr: errors.New("KVM unavailable")}
	err := runMicroVMCommand(t.Context(), []string{"doctor"}, strings.NewReader(""), io.Discard, mgr, false)
	if err == nil || !strings.Contains(err.Error(), "rerun 'mecated microvm doctor' (or 'mecatui microvm doctor')") || strings.Contains(err.Error(), "microvm status") {
		t.Fatalf("doctor failure guidance = %v", err)
	}
}

func TestMicroVMDoctorPrintsStructuredWarningsOnce(t *testing.T) {
	mgr := &fakeMicroVMManager{doctorReport: "PASS hypervisor ready; remediation: none\nWARN stale-resources 2 stale resources; remediation: inspect retained worktrees\n"}
	var out strings.Builder
	if err := runMicroVMCommand(t.Context(), []string{"doctor"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "WARN stale-resources") != 1 || !strings.Contains(out.String(), "inspect retained worktrees") {
		t.Fatalf("doctor warning was not surfaced exactly once:\n%s", out.String())
	}
	if strings.Contains(out.String(), "microvm-local is ready") {
		t.Fatalf("generic success duplicated the structured report:\n%s", out.String())
	}
}

func TestMicroVMDeleteFindsTargetAfterFirstStatusPage(t *testing.T) {
	first := make([]microvmmanager.Generation, 64)
	for i := range first {
		first[i] = microvmmanager.Generation{SessionID: fmt.Sprintf("s-%03d", i), Ref: fmt.Sprintf("env-%03d@1", i), Generation: 1}
	}
	mgr := &fakeMicroVMManager{statusPages: map[string]microvmmanager.Status{
		"":       {Configured: true, Running: true, Generations: first, Continuation: "page-2"},
		"page-2": {Configured: true, Running: true, Generations: []microvmmanager.Generation{{SessionID: "target", Ref: "env-target@9", Generation: 9}}},
	}}
	var out strings.Builder
	args := []string{"delete", "--session", "target", "--ref", "env-target@9", "--generation", "9", "--yes"}
	if err := runMicroVMCommand(t.Context(), args, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	if mgr.statusCalls != 2 || mgr.deleteCalls != 1 {
		t.Fatalf("status calls=%d delete calls=%d, want 2 and 1", mgr.statusCalls, mgr.deleteCalls)
	}
}

func TestMecatuiMicroVMCompatibilityFrontendUsesSharedImplementation(t *testing.T) {
	manager := &fakeMicroVMManager{status: microvmmanager.Status{Configured: true, Running: true, Continuation: "next"}}
	var compatibility, canonical strings.Builder
	args := []string{"status", "--output", "json"}
	if err := runMicroVMCommand(t.Context(), args, strings.NewReader(""), &compatibility, manager, false); err != nil {
		t.Fatal(err)
	}
	if err := microvmcmd.Run(t.Context(), microvmcmd.FrontendMecatui, args, strings.NewReader(""), &canonical, manager, false); err != nil {
		t.Fatal(err)
	}
	if compatibility.String() != canonical.String() {
		t.Fatalf("compatibility output differs:\n%s\ncanonical:\n%s", compatibility.String(), canonical.String())
	}

	compatibility.Reset()
	if err := runMicroVMCommand(t.Context(), []string{"--help"}, strings.NewReader(""), &compatibility, manager, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current local OS principal", "never targets a remote server", "compatibility frontend"} {
		if !strings.Contains(compatibility.String(), want) {
			t.Fatalf("compatibility help omitted %q:\n%s", want, compatibility.String())
		}
	}
}

type fakeMicroVMManager struct {
	status        microvmmanager.Status
	statusPages   map[string]microvmmanager.Status
	statusRequest microvmmanager.StatusRequest
	statusCalls   int
	doctorReport  string
	doctorErr     error
	deleteRequest microvmmanager.DeleteRequest
	deleteResult  microvmmanager.DeleteResult
	readyEndpoint string
	readyErr      error
	ensureCalls   int
	deleteCalls   int
}

func (f *fakeMicroVMManager) EnsureReady(context.Context, microvmmanager.ReadyRequest) (string, error) {
	f.ensureCalls++
	return f.readyEndpoint, f.readyErr
}
func (f *fakeMicroVMManager) Doctor(context.Context) (string, error) {
	return f.doctorReport, f.doctorErr
}
func (f *fakeMicroVMManager) Status(_ context.Context, requests ...microvmmanager.StatusRequest) (microvmmanager.Status, error) {
	f.statusCalls++
	if len(requests) > 0 {
		f.statusRequest = requests[0]
		if f.statusPages != nil {
			return f.statusPages[requests[0].Continuation], nil
		}
	}
	return f.status, nil
}
func (f *fakeMicroVMManager) Delete(_ context.Context, request microvmmanager.DeleteRequest) (microvmmanager.DeleteResult, error) {
	f.deleteCalls++
	f.deleteRequest = request
	return f.deleteResult, nil
}

var _ microVMManager = (*fakeMicroVMManager)(nil)
