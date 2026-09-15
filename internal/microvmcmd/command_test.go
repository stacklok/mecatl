package microvmcmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func TestStatusJSONIsTypedAndKeepsContinuationAsData(t *testing.T) {
	mgr := &fakeManager{status: microvmmanager.Status{
		Configured: true, Running: true, Socket: "/run/microvmd.sock", Continuation: "opaque token",
		Generations: []microvmmanager.Generation{{SessionID: "attachment-1", EnvironmentID: "e1", Ref: "e1@7", Generation: 7, WorktreePath: "/work/s1", State: "ready", Health: "healthy"}},
	}}
	var out strings.Builder
	if err := Run(t.Context(), []string{"status", "--page-size", "1", "--output", "json"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Backend      string `json:"backend"`
		Continuation string `json:"continuation"`
		Generations  []struct {
			AttachmentID string `json:"attachment_id"`
			Generation   uint32 `json:"generation"`
		} `json:"generations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Backend != microvmmanager.Alias || got.Continuation != "opaque token" || len(got.Generations) != 1 || got.Generations[0].AttachmentID != "attachment-1" || got.Generations[0].Generation != 7 {
		t.Fatalf("status JSON = %+v", got)
	}
	if strings.Contains(out.String(), "session_id") || strings.Contains(out.String(), `"profile"`) || strings.Contains(out.String(), "next page") || mgr.statusRequest.PageSize != 1 {
		t.Fatalf("status JSON exposed stale names/prose or wrong request: %q %+v", out.String(), mgr.statusRequest)
	}
}

func TestStatusJSONKeepsStoppedStateOnError(t *testing.T) {
	mgr := &fakeManager{
		status:    microvmmanager.Status{Configured: true, Running: false, Socket: "/run/microvmd.sock", GuestEgress: "deny-all"},
		statusErr: errors.New("configured microvmd is not serving at /private/socket"),
	}
	var out strings.Builder
	err := Run(t.Context(), []string{"status", "--output", "json"}, strings.NewReader(""), &out, mgr, false)
	if err == nil {
		t.Fatal("stopped status returned success")
	}
	var got statusJSON
	if json.Unmarshal([]byte(out.String()), &got) != nil {
		t.Fatalf("invalid status JSON: %q", out.String())
	}
	if got.State != "stopped" || got.Error != "daemon_not_running" || !strings.Contains(got.Remediation, "mecated microvm doctor") || got.Running {
		t.Fatalf("stopped status = %+v", got)
	}
	if strings.Contains(out.String(), "/private/socket") {
		t.Fatalf("status JSON leaked raw failure detail: %q", out.String())
	}
}

func TestStatusTextIsLocalReadOnlyAndCanonical(t *testing.T) {
	mgr := &fakeManager{status: microvmmanager.Status{
		Configured: true, Running: true, Continuation: "opaque token",
		Generations: []microvmmanager.Generation{{SessionID: "attachment-1", Ref: "env@2", Generation: 2, Health: "healthy", State: "ready"}},
	}}
	var out strings.Builder
	if err := Run(t.Context(), []string{"status"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"backend: microvm-local",
		"logical worktree: attachment_id=attachment-1",
		"next page: mecated microvm status --continuation \"opaque token\"",
		"status is read-only",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status omitted %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "profile:") || strings.Contains(out.String(), "session=") || strings.Contains(out.String(), "mecatui microvm") {
		t.Fatalf("status exposed stale terminology:\n%s", out.String())
	}
}

func TestStatusEmptyStateIsExplicitAndActionable(t *testing.T) {
	var out strings.Builder
	if err := Run(t.Context(), []string{"status"}, strings.NewReader(""), &out, &fakeManager{}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backend state: not configured; run mecated microvm doctor to check host readiness", "status is read-only", "default_placement: microvm-local", "guest IPv4 egress defaults to permissive", `mecated serve --headless --default-placement microvm-local`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("fresh status omitted %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorJSONReportsSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		err     error
		success bool
	}{{nil, true}, {errors.New("KVM unavailable"), false}} {
		mgr := &fakeManager{doctorReport: "PASS kvm\n", doctorErr: tc.err}
		var out strings.Builder
		err := Run(t.Context(), []string{"doctor", "--output", "json"}, strings.NewReader(""), &out, mgr, false)
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

func TestDeleteRequiresExactTargetAndConfirmation(t *testing.T) {
	mgr := &fakeManager{status: microvmmanager.Status{Generations: []microvmmanager.Generation{{SessionID: "attachment-1", Ref: "env-1@7", Generation: 7}}}}
	var out strings.Builder
	err := Run(t.Context(), []string{
		"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7", "--yes",
	}, strings.NewReader(""), &out, mgr, false)
	if err != nil {
		t.Fatal(err)
	}
	if mgr.deleteCalls != 1 || mgr.deleteRequest.SessionID != "attachment-1" || !strings.Contains(out.String(), "repository VM retained") {
		t.Fatalf("delete calls=%d request=%+v output=%q", mgr.deleteCalls, mgr.deleteRequest, out.String())
	}
}

func TestDeleteRejectsIncompleteMismatchedAndUnconfirmedTargets(t *testing.T) {
	target := microvmmanager.Generation{SessionID: "attachment-1", Ref: "env-1@7", Generation: 7}
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing confirmation", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7"}},
		{name: "wrong backend", args: []string{"delete", "--backend", "other", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7", "--yes"}},
		{name: "missing attachment", args: []string{"delete", "--backend", "microvm-local", "--ref", "env-1@7", "--generation", "7", "--yes"}},
		{name: "wrong attachment", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-2", "--ref", "env-1@7", "--generation", "7", "--yes"}},
		{name: "missing ref", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--generation", "7", "--yes"}},
		{name: "wrong ref", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-2@7", "--generation", "7", "--yes"}},
		{name: "missing generation", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--yes"}},
		{name: "wrong generation", args: []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@8", "--generation", "8", "--yes"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeManager{status: microvmmanager.Status{Generations: []microvmmanager.Generation{target}}}
			err := Run(t.Context(), tc.args, strings.NewReader(""), &strings.Builder{}, mgr, false)
			if err == nil {
				t.Fatal("unsafe delete succeeded")
			}
			if mgr.deleteCalls != 0 {
				t.Fatalf("unsafe delete reached mutation %d times", mgr.deleteCalls)
			}
		})
	}
}

func TestDeleteValidationTraversesPagesAndRejectsAmbiguity(t *testing.T) {
	target := microvmmanager.Generation{SessionID: "attachment-1", Ref: "env-1@7", Generation: 7}
	t.Run("later page", func(t *testing.T) {
		mgr := &fakeManager{statusPages: map[string]microvmmanager.Status{
			"":     {Continuation: "next"},
			"next": {Generations: []microvmmanager.Generation{target}},
		}}
		err := Run(t.Context(), []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7", "--yes"}, strings.NewReader(""), &strings.Builder{}, mgr, false)
		if err != nil || mgr.deleteCalls != 1 || mgr.statusCalls != 2 {
			t.Fatalf("paged delete err=%v status=%d delete=%d", err, mgr.statusCalls, mgr.deleteCalls)
		}
	})
	t.Run("duplicate target", func(t *testing.T) {
		mgr := &fakeManager{statusPages: map[string]microvmmanager.Status{
			"":     {Generations: []microvmmanager.Generation{target}, Continuation: "next"},
			"next": {Generations: []microvmmanager.Generation{target}},
		}}
		err := Run(t.Context(), []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7", "--yes"}, strings.NewReader(""), &strings.Builder{}, mgr, false)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || mgr.deleteCalls != 0 {
			t.Fatalf("ambiguous delete err=%v delete=%d", err, mgr.deleteCalls)
		}
	})
	t.Run("repeated continuation", func(t *testing.T) {
		mgr := &fakeManager{statusPages: map[string]microvmmanager.Status{
			"":     {Continuation: "next"},
			"next": {Continuation: "next"},
		}}
		err := Run(t.Context(), []string{"delete", "--backend", "microvm-local", "--attachment-id", "attachment-1", "--ref", "env-1@7", "--generation", "7", "--yes"}, strings.NewReader(""), &strings.Builder{}, mgr, false)
		if err == nil || !strings.Contains(err.Error(), "repeated continuation") || mgr.deleteCalls != 0 {
			t.Fatalf("cyclic pagination err=%v delete=%d", err, mgr.deleteCalls)
		}
	})
}

func TestHelpIsReadOnlyLocalAndSideEffectFree(t *testing.T) {
	mgr := &fakeManager{}
	var out strings.Builder
	if err := Run(t.Context(), []string{"--help-all"}, strings.NewReader(""), &out, mgr, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Usage: mecated microvm doctor|status|delete",
		"current OS principal",
		"local execution host",
		"doctor    read-only",
		"status    read-only",
		"delete    remove one exact logical attachment",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help omitted %q: %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "mecatui microvm") {
		t.Fatalf("help advertised noncanonical frontend: %q", out.String())
	}
	if mgr.doctorCalls != 0 || mgr.statusCalls != 0 {
		t.Fatal("help called manager")
	}
}

func TestSubcommandHelpIsSideEffectFreeAndShowsEssentialFlags(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{command: "doctor", want: []string{"current OS principal", "fresh home", "--output text|json"}},
		{command: "status", want: []string{"current OS principal", "owner-scoped", "--page-size 1..64", "--continuation TOKEN", "--output text|json"}},
	} {
		mgr := &fakeManager{}
		var out strings.Builder
		if err := Run(t.Context(), []string{tc.command, "--help"}, strings.NewReader(""), &out, mgr, false); err != nil {
			t.Fatalf("%s --help: %v", tc.command, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s help omitted %q:\n%s", tc.command, want, out.String())
			}
		}
		if mgr.doctorCalls != 0 || mgr.statusCalls != 0 {
			t.Fatalf("%s help called manager", tc.command)
		}
	}
}

func TestRejectsUnknownOutputFormat(t *testing.T) {
	err := Run(t.Context(), []string{"status", "--output", "yaml"}, strings.NewReader(""), &strings.Builder{}, &fakeManager{}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown output format") {
		t.Fatalf("error = %v", err)
	}
}

type fakeManager struct {
	status        microvmmanager.Status
	statusPages   map[string]microvmmanager.Status
	statusErr     error
	statusRequest microvmmanager.StatusRequest
	statusCalls   int
	doctorReport  string
	doctorErr     error
	doctorCalls   int
	deleteRequest microvmmanager.DeleteRequest
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
		if f.statusPages != nil {
			return f.statusPages[requests[0].Continuation], f.statusErr
		}
	}
	return f.status, f.statusErr
}

func (f *fakeManager) Delete(_ context.Context, request microvmmanager.DeleteRequest) (microvmmanager.DeleteResult, error) {
	f.deleteCalls++
	f.deleteRequest = request
	return f.deleteResult, nil
}
