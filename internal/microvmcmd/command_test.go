package microvmcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func TestStatusJSONIsTypedAndKeepsContinuationAsData(t *testing.T) {
	mgr := &fakeManager{status: microvmmanager.Status{
		Configured: true, Running: true, Socket: "/run/microvmd.sock", Continuation: "opaque token",
		Generations: []microvmmanager.Generation{{SessionID: "s1", EnvironmentID: "e1", Ref: "e1@7", Generation: 7, WorktreePath: "/work/s1", State: "ready", Health: "healthy"}},
	}}
	var out strings.Builder
	if err := Run(t.Context(), FrontendMecated, []string{"status", "--page-size", "1", "--output", "json"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Profile      string `json:"profile"`
		Continuation string `json:"continuation"`
		Generations  []struct {
			SessionID  string `json:"session_id"`
			Generation uint32 `json:"generation"`
		} `json:"generations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Profile != microvmmanager.Alias || got.Continuation != "opaque token" || len(got.Generations) != 1 || got.Generations[0].SessionID != "s1" || got.Generations[0].Generation != 7 {
		t.Fatalf("status JSON = %+v", got)
	}
	if strings.Contains(out.String(), "next page") || mgr.statusRequest.PageSize != 1 {
		t.Fatalf("JSON contains prose or wrong request: %q %+v", out.String(), mgr.statusRequest)
	}
}

func TestStatusContinuationNamesOnlyInvokingFrontend(t *testing.T) {
	for _, frontend := range []Frontend{FrontendMecated, FrontendMecatui} {
		t.Run(string(frontend), func(t *testing.T) {
			mgr := &fakeManager{status: microvmmanager.Status{Configured: true, Running: true, Continuation: "opaque token"}}
			var out strings.Builder
			if err := Run(t.Context(), frontend, []string{"status"}, strings.NewReader(""), &out, mgr, false); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("next page: %s microvm status --continuation %q", frontend, "opaque token")
			if !strings.Contains(out.String(), want) {
				t.Fatalf("status omitted invoking frontend continuation %q:\n%s", want, out.String())
			}
			other := FrontendMecated
			if frontend == FrontendMecated {
				other = FrontendMecatui
			}
			if strings.Contains(out.String(), string(other)+" microvm status --continuation") {
				t.Fatalf("status named uninvoked frontend %q:\n%s", other, out.String())
			}
		})
	}
}

func TestStatusEmptyStateIsExplicitAndActionable(t *testing.T) {
	var out strings.Builder
	if err := Run(t.Context(), FrontendMecated, []string{"status"}, strings.NewReader(""), &out, &fakeManager{}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"backend state: not configured",
		"status is read-only",
		"mecatui --default-placement microvm-local",
		`mecated serve --headless --default-placement microvm-local`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("fresh status omitted %q:\n%s", want, out.String())
		}
	}
}

func TestDeleteRequiresExactSelectorAndExplicitConsent(t *testing.T) {
	mgr := &fakeManager{status: microvmmanager.Status{Generations: []microvmmanager.Generation{{SessionID: "s1", Ref: "env@2", Generation: 2}}}}
	for _, args := range [][]string{
		{"delete", "--session", "s1", "--ref", "env@2", "--generation", "2"},
		{"delete", "--session", "s1", "--ref", "env@3", "--generation", "2", "--yes"},
	} {
		if err := Run(t.Context(), FrontendMecated, args, strings.NewReader("yes\n"), &strings.Builder{}, mgr, false); err == nil {
			t.Fatalf("Run(%v) succeeded", args)
		}
	}
	if mgr.deleteCalls != 0 {
		t.Fatalf("delete calls = %d", mgr.deleteCalls)
	}
}

func TestDeleteJSONReportsSelectorAndDirtyRetention(t *testing.T) {
	mgr := &fakeManager{
		status:       microvmmanager.Status{Generations: []microvmmanager.Generation{{SessionID: "s1", Ref: "env@2", Generation: 2}}},
		deleteResult: microvmmanager.DeleteResult{WorktreePath: "/work/s1", WorktreeRetained: true},
	}
	var out strings.Builder
	err := Run(t.Context(), FrontendMecated, []string{"delete", "--session", "s1", "--ref", "env@2", "--generation", "2", "--yes", "--output", "json"}, strings.NewReader(""), &out, mgr, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Selector struct {
			SessionID string `json:"session_id"`
		} `json:"selector"`
		Result struct {
			WorktreePath string `json:"worktree_path"`
		} `json:"result"`
		DirtyRetained bool `json:"dirty_retained"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Selector.SessionID != "s1" || got.Result.WorktreePath != "/work/s1" || !got.DirtyRetained {
		t.Fatalf("delete JSON = %+v", got)
	}
}

func TestDoctorJSONReportsSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		err     error
		success bool
	}{{nil, true}, {errors.New("KVM unavailable"), false}} {
		mgr := &fakeManager{doctorReport: "PASS kvm\n", doctorErr: tc.err}
		var out strings.Builder
		err := Run(t.Context(), FrontendMecated, []string{"doctor", "--output", "json"}, strings.NewReader(""), &out, mgr, false)
		if (err == nil) != tc.success {
			t.Fatalf("err = %v, success=%t", err, tc.success)
		}
		var got struct {
			Success bool   `json:"success"`
			Report  string `json:"report"`
		}
		if json.Unmarshal([]byte(out.String()), &got) != nil || got.Success != tc.success || got.Report != "PASS kvm\n" {
			t.Fatalf("doctor JSON = %q", out.String())
		}
	}
}

func TestStableJSONAndTextOutput(t *testing.T) {
	status := microvmmanager.Status{
		Configured:  true,
		Running:     true,
		Socket:      "/run/microvmd.sock",
		GuestEgress: "deny-all",
		Generations: []microvmmanager.Generation{{
			SessionID: "s1", EnvironmentID: "env1", Ref: "env1@7", Generation: 7,
			WorktreePath: "/work/s1", State: "ready", Health: "healthy", Error: "",
		}},
		Continuation: "next-token",
	}
	selector := []string{"--session", "s1", "--ref", "env1@7", "--generation", "7", "--yes"}
	tests := []struct {
		name       string
		args       []string
		manager    *fakeManager
		wantOutput string
		wantErr    bool
	}{
		{
			name: "status json", args: []string{"status", "--output", "json"}, manager: &fakeManager{status: status},
			wantOutput: "{\"profile\":\"microvm-local\",\"configured\":true,\"running\":true,\"socket\":\"/run/microvmd.sock\",\"guest_egress\":\"deny-all\",\"generations\":[{\"session_id\":\"s1\",\"environment_id\":\"env1\",\"ref\":\"env1@7\",\"generation\":7,\"worktree_path\":\"/work/s1\",\"state\":\"ready\",\"health\":\"healthy\",\"error\":\"\"}],\"continuation\":\"next-token\"}\n",
		},
		{
			name: "status text", args: []string{"status"}, manager: &fakeManager{status: status},
			wantOutput: "profile: microvm-local\nconfigured: true\ndaemon running: true\nsocket: /run/microvmd.sock\nguest egress: deny-all\nlogical worktree: session=s1 ref=env1@7 repository-generation=7 health=healthy state=ready worktree=/work/s1\nnext page: mecated microvm status --continuation \"next-token\"\nstatus is read-only; it never installs, starts, stops, or deletes microVM state\n",
		},
		{
			name: "doctor success json", args: []string{"doctor", "--output", "json"}, manager: &fakeManager{doctorReport: "PASS kvm\n"},
			wantOutput: "{\"success\":true,\"report\":\"PASS kvm\\n\",\"error\":\"\"}\n",
		},
		{
			name: "doctor success text", args: []string{"doctor"}, manager: &fakeManager{doctorReport: "PASS kvm\n"},
			wantOutput: "PASS kvm\n",
		},
		{
			name: "doctor failure json", args: []string{"doctor", "--output", "json"}, manager: &fakeManager{doctorReport: "FAIL kvm\n", doctorErr: errors.New("KVM unavailable")},
			wantOutput: "{\"success\":false,\"report\":\"FAIL kvm\\n\",\"error\":\"KVM unavailable\"}\n", wantErr: true,
		},
		{
			name: "doctor failure text", args: []string{"doctor"}, manager: &fakeManager{doctorReport: "FAIL kvm\n", doctorErr: errors.New("KVM unavailable")},
			wantOutput: "FAIL kvm\n", wantErr: true,
		},
		{
			name: "clean delete json", args: append([]string{"delete"}, append(selector, "--output", "json")...), manager: deleteManager(false),
			wantOutput: "{\"selector\":{\"session_id\":\"s1\",\"ref\":\"env1@7\",\"generation\":7},\"result\":{\"worktree_path\":\"/work/s1\",\"worktree_removed\":true,\"repository_vm_retained\":true},\"dirty_retained\":false}\n",
		},
		{
			name: "dirty delete json", args: append([]string{"delete"}, append(selector, "--output", "json")...), manager: deleteManager(true),
			wantOutput: "{\"selector\":{\"session_id\":\"s1\",\"ref\":\"env1@7\",\"generation\":7},\"result\":{\"worktree_path\":\"/work/s1\",\"worktree_removed\":false,\"repository_vm_retained\":true},\"dirty_retained\":true}\n",
		},
		{
			name: "clean delete text", args: append([]string{"delete"}, selector...), manager: deleteManager(false),
			wantOutput: "Permanently delete logical worktree session=s1 ref=env1@7 repository-generation=7. The shared repository VM is not deleted. Closing a host client normally only detaches and preserves this worktree. Dirty worktrees are retained.\nlogical attachment and clean worktree removed; repository VM retained: /work/s1\n",
		},
		{
			name: "dirty delete text", args: append([]string{"delete"}, selector...), manager: deleteManager(true),
			wantOutput: "Permanently delete logical worktree session=s1 ref=env1@7 repository-generation=7. The shared repository VM is not deleted. Closing a host client normally only detaches and preserves this worktree. Dirty worktrees are retained.\nlogical attachment deleted; dirty worktree retained: /work/s1\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			err := Run(t.Context(), FrontendMecated, tc.args, strings.NewReader(""), &out, tc.manager, false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tc.wantErr)
			}
			if got := out.String(); got != tc.wantOutput {
				t.Fatalf("output mismatch\n got: %q\nwant: %q", got, tc.wantOutput)
			}
		})
	}
}

func deleteManager(retained bool) *fakeManager {
	return &fakeManager{
		status:       microvmmanager.Status{Generations: []microvmmanager.Generation{{SessionID: "s1", Ref: "env1@7", Generation: 7}}},
		deleteResult: microvmmanager.DeleteResult{WorktreePath: "/work/s1", WorktreeRetained: retained},
	}
}

func TestDeletePaginationFailuresDoNotPromptOrDelete(t *testing.T) {
	tests := []struct {
		name      string
		status    func(microvmmanager.StatusRequest) microvmmanager.Status
		wantCalls int
		wantError string
	}{
		{
			name: "repeated continuation",
			status: func(microvmmanager.StatusRequest) microvmmanager.Status {
				return microvmmanager.Status{Continuation: "same-token"}
			},
			wantCalls: 2, wantError: "repeated continuation",
		},
		{
			name: "page cap",
			status: func(request microvmmanager.StatusRequest) microvmmanager.Status {
				return microvmmanager.Status{Continuation: fmt.Sprintf("page-%04d", requestCount(request.Continuation)+1)}
			},
			wantCalls: 1024, wantError: "exceeded 1024 status pages",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeManager{statusFunc: tc.status}
			var out strings.Builder
			err := Run(t.Context(), FrontendMecated, []string{"delete", "--session", "s1", "--ref", "env@1", "--generation", "1"}, strings.NewReader("yes\n"), &out, mgr, true)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if mgr.statusCalls != tc.wantCalls || mgr.deleteCalls != 0 || strings.Contains(out.String(), "[y/N]") {
				t.Fatalf("status calls=%d delete calls=%d output=%q", mgr.statusCalls, mgr.deleteCalls, out.String())
			}
		})
	}
}

func requestCount(continuation string) int {
	if continuation == "" {
		return 0
	}
	var count int
	_, _ = fmt.Sscanf(continuation, "page-%04d", &count)
	return count
}

func TestDeleteNoninteractiveNeverReadsConsent(t *testing.T) {
	mgr := deleteManager(false)
	var out strings.Builder
	err := Run(t.Context(), FrontendMecated, []string{"delete", "--session", "s1", "--ref", "env1@7", "--generation", "7"}, panicReader{}, &out, mgr, false)
	if err == nil || err.Error() != "microVM delete requires confirmation (--yes for noninteractive use)" {
		t.Fatalf("error = %v", err)
	}
	if mgr.deleteCalls != 0 {
		t.Fatalf("delete calls = %d", mgr.deleteCalls)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("noninteractive confirmation read stdin") }

func TestHelpAllIsAccepted(t *testing.T) {
	var out strings.Builder
	if err := Run(t.Context(), FrontendMecated, []string{"--help-all"}, strings.NewReader(""), &out, &fakeManager{}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Usage: mecated microvm doctor|status|delete",
		"mecatui microvm doctor|status|delete",
		"doctor    read-only",
		"status    read-only",
		"mecatui --default-placement microvm-local",
		`mecated serve --headless --default-placement microvm-local`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help omitted %q: %q", want, out.String())
		}
	}
}

func TestSubcommandHelpIsSideEffectFreeAndShowsEssentialFlags(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{command: "doctor", want: []string{"never downloads", "--output text|json"}},
		{command: "status", want: []string{"Read-only owner-scoped", "--page-size 1..64", "--continuation TOKEN", "--output text|json"}},
		{command: "delete", want: []string{"Copy all three selector values", "--session ID", "--ref REF", "--generation N", "--yes", "--output text|json"}},
	} {
		mgr := &fakeManager{}
		var out strings.Builder
		if err := Run(t.Context(), FrontendMecated, []string{tc.command, "--help"}, strings.NewReader(""), &out, mgr, false); err != nil {
			t.Fatalf("%s --help: %v", tc.command, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s help omitted %q:\n%s", tc.command, want, out.String())
			}
		}
		if mgr.doctorCalls != 0 || mgr.statusCalls != 0 || mgr.deleteCalls != 0 {
			t.Fatalf("%s help called manager: doctor=%d status=%d delete=%d", tc.command, mgr.doctorCalls, mgr.statusCalls, mgr.deleteCalls)
		}
	}
	err := Run(t.Context(), FrontendMecated, []string{"wat"}, strings.NewReader(""), &strings.Builder{}, &fakeManager{}, false)
	if err == nil || !strings.Contains(err.Error(), "mecated microvm --help") || !strings.Contains(err.Error(), "mecatui microvm --help") {
		t.Fatalf("unknown command guidance = %v", err)
	}
}

func TestRejectsUnknownOutputFormat(t *testing.T) {
	err := Run(t.Context(), FrontendMecated, []string{"status", "--output", "yaml"}, strings.NewReader(""), &strings.Builder{}, &fakeManager{}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown output format") {
		t.Fatalf("error = %v", err)
	}
}

type fakeManager struct {
	status        microvmmanager.Status
	statusFunc    func(microvmmanager.StatusRequest) microvmmanager.Status
	statusRequest microvmmanager.StatusRequest
	statusCalls   int
	doctorReport  string
	doctorErr     error
	doctorCalls   int
	deleteResult  microvmmanager.DeleteResult
	deleteCalls   int
}

func (f *fakeManager) Doctor(context.Context) (string, error) {
	f.doctorCalls++
	return f.doctorReport, f.doctorErr
}
func (f *fakeManager) Status(_ context.Context, requests ...microvmmanager.StatusRequest) (microvmmanager.Status, error) {
	f.statusCalls++
	if len(requests) > 0 {
		f.statusRequest = requests[0]
		if f.statusFunc != nil {
			return f.statusFunc(requests[0]), nil
		}
	}
	return f.status, nil
}
func (f *fakeManager) Delete(context.Context, microvmmanager.DeleteRequest) (microvmmanager.DeleteResult, error) {
	f.deleteCalls++
	return f.deleteResult, nil
}
