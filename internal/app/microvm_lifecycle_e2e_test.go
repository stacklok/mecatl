package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementTestDaemon struct {
	listener      net.Listener
	root          string
	mu            sync.Mutex
	ops           []string
	next          int
	guests        map[string]string
	claims        map[string]string
	attached      map[string]bool
	acquisitions  map[string]string
	nextAcquire   uint64
	rejectResolve bool
	seed          func(string) error
	execBindings  []string
	deleteClaims  []string
}

func startPlacementTestDaemon(t *testing.T) *placementTestDaemon {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "mecatl-microvm-")
	if err != nil {
		t.Fatalf("create short private socket directory: %v", err)
	}
	socketPath := filepath.Join(socketDir, "microvmd.sock")
	t.Cleanup(func() {
		_ = os.Remove(socketPath)
		if err := os.Remove(socketDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("remove short private socket directory: %v", err)
		}
	})
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	d := &placementTestDaemon{listener: listener, root: t.TempDir(), guests: make(map[string]string), claims: make(map[string]string), attached: make(map[string]bool), acquisitions: make(map[string]string)}
	t.Cleanup(func() { _ = listener.Close() })
	go d.serve()
	return d
}

func (d *placementTestDaemon) endpoint() string { return "unix://" + d.listener.Addr().String() }

func (d *placementTestDaemon) serve() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.serveConn(conn)
	}
}

func (d *placementTestDaemon) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var request map[string]any
	if readPlacementFrame(conn, &request) != nil {
		return
	}
	op, _ := request["operation"].(string)
	binding, _ := request["binding"].(map[string]any)
	d.mu.Lock()
	d.ops = append(d.ops, op)
	d.mu.Unlock()

	response := map[string]any{"binding": binding}
	switch op {
	case "create":
		provision, _ := request["provision"].(map[string]any)
		sessionID, _ := provision["session_id"].(string)
		d.mu.Lock()
		d.next++
		environmentID := fmt.Sprintf("logical-%d", d.next)
		guestRoot := filepath.Join(d.root, environmentID)
		d.guests[environmentID] = guestRoot
		d.mu.Unlock()
		if err := os.MkdirAll(guestRoot, 0o700); err != nil {
			response = map[string]any{"error_code": "create_failed", "error": "guest root unavailable"}
			break
		}
		binding = map[string]any{"owner": provision["owner"], "session_id": sessionID, "environment_id": environmentID, "ref": environmentID + "@7", "generation": 7}
		d.mu.Lock()
		d.claims[environmentID] = placementClaimKey(binding)
		d.attached[environmentID] = true
		seed := d.seed
		d.mu.Unlock()
		if seed != nil {
			if err := seed(guestRoot); err != nil {
				response = map[string]any{"error_code": "create_failed", "error": "guest seed failed"}
				break
			}
		}
		response["binding"] = binding
		response["created"] = map[string]any{
			"ref": map[string]any{"Kind": "microvm", "ID": environmentID + "@7"}, "generation": 7,
			"host_worktree": guestRoot, "guest_root": "/workspace", "profile": "microvm-local",
			"guest_egress": "deny-all", "host_egress": "not constrained",
		}
	case "resolve":
		environmentID, _ := binding["environment_id"].(string)
		d.mu.Lock()
		if d.rejectResolve || d.guests[environmentID] == "" || d.claims[environmentID] != placementClaimKey(binding) {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
		} else {
			d.attached[environmentID] = true
		}
		d.mu.Unlock()
	case "detach", "child-delete":
		if !d.hasBinding(binding) {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
		} else {
			environmentID, _ := binding["environment_id"].(string)
			d.mu.Lock()
			delete(d.attached, environmentID)
			d.mu.Unlock()
		}
	case "delete":
		environmentID, _ := binding["environment_id"].(string)
		d.mu.Lock()
		guestRoot, ok := d.guests[environmentID]
		ok = ok && d.claims[environmentID] == placementClaimKey(binding)
		d.mu.Unlock()
		if !ok {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
			break
		}
		if err := os.Remove(guestRoot); err != nil {
			response = map[string]any{"error_code": "delete_failed", "error": "guest root cleanup failed"}
			break
		}
		ref, _ := binding["ref"].(string)
		sessionID, _ := binding["session_id"].(string)
		generation, _ := binding["generation"].(float64)
		d.mu.Lock()
		delete(d.guests, environmentID)
		d.deleteClaims = append(d.deleteClaims, fmt.Sprintf("%s/%s/%s/%s/%.0f", binding["owner"], sessionID, environmentID, ref, generation))
		d.mu.Unlock()
		payload, _ := json.Marshal(map[string]bool{"worktree_retained": false})
		response = map[string]any{"binding": binding, "payload": json.RawMessage(payload)}
	case "fork":
		parentRoot, ok := d.guestRoot(binding)
		if !ok {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
			break
		}
		d.mu.Lock()
		d.next++
		childID := fmt.Sprintf("logical-%d", d.next)
		childRoot := filepath.Join(d.root, childID)
		d.guests[childID] = childRoot
		d.mu.Unlock()
		if err := copyPlacementTree(parentRoot, childRoot); err != nil {
			response = map[string]any{"error_code": "fork_failed", "error": "child root unavailable"}
			break
		}
		var forkPayload struct {
			Label string `json:"label"`
		}
		encoded, _ := json.Marshal(request["payload"])
		var raw json.RawMessage
		_ = json.Unmarshal(encoded, &raw)
		if json.Unmarshal(raw, &forkPayload) != nil || forkPayload.Label == "" {
			response = map[string]any{"error_code": "invalid", "error": "invalid fork label"}
			break
		}
		childBinding := map[string]any{
			"owner": binding["owner"], "session_id": fmt.Sprint(binding["session_id"]) + ":" + forkPayload.Label,
			"environment_id": childID, "ref": childID + "@7", "generation": 7,
		}
		d.mu.Lock()
		d.claims[childID] = placementClaimKey(childBinding)
		d.attached[childID] = true
		d.mu.Unlock()
		response["binding"] = childBinding
		binding = childBinding
	case "workspace":
		guestRoot, ok := d.guestRoot(binding)
		if !ok {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
			break
		}
		var payload struct {
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Pattern   string `json:"pattern"`
		}
		encoded, _ := json.Marshal(request["payload"])
		var raw json.RawMessage
		_ = json.Unmarshal(encoded, &raw)
		if json.Unmarshal(raw, &payload) != nil {
			response = map[string]any{"error_code": "invalid", "error": "invalid workspace request"}
			break
		}
		workspaceResult := map[string]any{}
		requestedPath := payload.Path
		if payload.Operation == "glob" {
			requestedPath = payload.Pattern
		}
		cleanedPath := filepath.Clean(filepath.FromSlash(requestedPath))
		if filepath.IsAbs(cleanedPath) || cleanedPath == ".." || strings.HasPrefix(cleanedPath, ".."+string(filepath.Separator)) {
			workspaceResult["error_code"] = "invalid"
			workspacePayload, _ := json.Marshal(workspaceResult)
			response["payload"] = json.RawMessage(workspacePayload)
			break
		}
		switch payload.Operation {
		case "read":
			data, readErr := os.ReadFile(filepath.Join(guestRoot, filepath.FromSlash(payload.Path)))
			if errors.Is(readErr, fs.ErrNotExist) {
				workspaceResult["error_code"] = "not_found"
			} else if readErr != nil {
				workspaceResult["error_code"] = "read_failed"
			} else {
				workspaceResult["data"] = data
				workspaceResult["version"] = "test-version"
				workspaceResult["version_valid"] = true
			}
		case "glob":
			matches, globErr := filepath.Glob(filepath.Join(guestRoot, filepath.FromSlash(payload.Pattern)))
			if globErr != nil {
				workspaceResult["error_code"] = "invalid"
				break
			}
			paths := make([]string, 0, len(matches))
			for _, match := range matches {
				rel, relErr := filepath.Rel(guestRoot, match)
				if relErr == nil {
					paths = append(paths, filepath.ToSlash(rel))
				}
			}
			workspaceResult["paths"] = paths
		default:
			workspaceResult["error_code"] = "unsupported"
		}
		workspacePayload, _ := json.Marshal(workspaceResult)
		response["payload"] = json.RawMessage(workspacePayload)
	case "exec":
		guestRoot, ok := d.guestRoot(binding)
		if !ok {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
			break
		}
		environmentID, _ := binding["environment_id"].(string)
		d.mu.Lock()
		d.execBindings = append(d.execBindings, environmentID)
		d.mu.Unlock()
		var payload struct {
			Command string `json:"command"`
		}
		encoded, _ := json.Marshal(request["payload"])
		var raw json.RawMessage
		_ = json.Unmarshal(encoded, &raw)
		if err := json.Unmarshal(raw, &payload); err != nil {
			response = map[string]any{"error_code": "invalid", "error": "invalid command"}
			break
		}
		command := exec.CommandContext(context.Background(), "/bin/sh", "-c", payload.Command)
		command.Dir = guestRoot
		command.Env = []string{"HOME=" + guestRoot, "PATH=/usr/bin:/bin"}
		stdout, runErr := command.Output()
		exitCode := 0
		var stderr []byte
		if runErr != nil {
			exitCode = 1
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				exitCode, stderr = exitErr.ExitCode(), exitErr.Stderr
			}
		}
		if len(stdout) != 0 {
			_ = writePlacementFrame(conn, map[string]any{"stream": map[string]any{"channel": "stdout", "data": stdout}})
		}
		if len(stderr) != 0 {
			_ = writePlacementFrame(conn, map[string]any{"stream": map[string]any{"channel": "stderr", "data": stderr}})
		}
		payloadOut, _ := json.Marshal(map[string]any{"exit_code": exitCode})
		response = map[string]any{"binding": binding, "payload": json.RawMessage(payloadOut)}
	}
	if (op == "create" || op == "resolve" || op == "fork") && response["error_code"] == nil {
		environmentID, _ := binding["environment_id"].(string)
		d.mu.Lock()
		d.nextAcquire++
		acquisitionID := fmt.Sprintf("%032x", d.nextAcquire)
		d.acquisitions[acquisitionID] = environmentID
		d.mu.Unlock()
		response["acquisition_id"] = acquisitionID
		if writePlacementFrame(conn, response) != nil {
			d.releaseTestAcquisition(acquisitionID, environmentID)
			return
		}
		var terminal map[string]any
		if readPlacementFrame(conn, &terminal) != nil {
			d.releaseTestAcquisition(acquisitionID, environmentID)
			return
		}
		terminalID, _ := terminal["acquisition_id"].(string)
		terminalBinding, _ := terminal["binding"].(map[string]any)
		terminalOp, _ := terminal["operation"].(string)
		if terminalID != acquisitionID || placementClaimKey(terminalBinding) != placementClaimKey(binding) || (terminalOp != "detach" && terminalOp != "delete" && terminalOp != "child-delete") {
			d.releaseTestAcquisition(acquisitionID, environmentID)
			_ = writePlacementFrame(conn, map[string]any{"error_code": "binding_mismatch", "error": "binding mismatch"})
			return
		}
		d.mu.Lock()
		d.ops = append(d.ops, terminalOp)
		otherOwners := 0
		for ownerID, heldEnvironment := range d.acquisitions {
			if ownerID != acquisitionID && heldEnvironment == environmentID {
				otherOwners++
			}
		}
		d.mu.Unlock()
		terminalResponse := map[string]any{"binding": binding, "acquisition_id": acquisitionID}
		if terminalOp == "delete" || terminalOp == "child-delete" {
			if otherOwners != 0 {
				terminalResponse = map[string]any{"binding": binding, "acquisition_id": acquisitionID, "error_code": "in_use", "error": "acquisition is in use"}
			} else {
				d.mu.Lock()
				guestRoot := d.guests[environmentID]
				delete(d.guests, environmentID)
				delete(d.claims, environmentID)
				d.deleteClaims = append(d.deleteClaims, fmt.Sprintf("%s/%s/%s/%s/%v", binding["owner"], binding["session_id"], environmentID, binding["ref"], binding["generation"]))
				d.mu.Unlock()
				_ = os.RemoveAll(guestRoot)
				payload, _ := json.Marshal(map[string]bool{"worktree_retained": false})
				terminalResponse["payload"] = json.RawMessage(payload)
			}
		}
		d.releaseTestAcquisition(acquisitionID, environmentID)
		_ = writePlacementFrame(conn, terminalResponse)
		return
	}
	_ = writePlacementFrame(conn, response)
}

func (d *placementTestDaemon) releaseTestAcquisition(id, environmentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.acquisitions, id)
	for _, heldEnvironment := range d.acquisitions {
		if heldEnvironment == environmentID {
			return
		}
	}
	delete(d.attached, environmentID)
}

func placementClaimKey(binding map[string]any) string {
	return fmt.Sprintf("%v/%v/%v/%v", binding["owner"], binding["session_id"], binding["ref"], binding["generation"])
}

func (d *placementTestDaemon) guestRoot(binding map[string]any) (string, bool) {
	environmentID, _ := binding["environment_id"].(string)
	d.mu.Lock()
	defer d.mu.Unlock()
	root, ok := d.guests[environmentID]
	return root, ok && d.attached[environmentID] && d.claims[environmentID] == placementClaimKey(binding)
}

func (d *placementTestDaemon) writeGuestFile(t *testing.T, environmentID, name, body string) {
	t.Helper()
	d.mu.Lock()
	root := d.guests[environmentID]
	d.mu.Unlock()
	if root == "" {
		t.Fatalf("guest %q is unavailable", environmentID)
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *placementTestDaemon) hasBinding(binding map[string]any) bool {
	_, ok := d.guestRoot(binding)
	return ok
}

func (d *placementTestDaemon) loseRuntimeState() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.guests = make(map[string]string)
}

func copyPlacementTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

func (d *placementTestDaemon) operations() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func (d *placementTestDaemon) operationCount(want string) int {
	count := 0
	for _, op := range d.operations() {
		if op == want {
			count++
		}
	}
	return count
}

func (d *placementTestDaemon) deleteClaimsSnapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.deleteClaims...)
}

func (d *placementTestDaemon) executedBindings() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.execBindings...)
}

type placementReadyManager struct {
	endpoint string
	calls    int
}

func (m *placementReadyManager) EnsureReady(context.Context, microvmmanager.ReadyRequest) (string, error) {
	m.calls++
	return m.endpoint, nil
}

func TestMicroVMDefaultPlacementRetainsOneExactAttachmentUntilCloseSession(t *testing.T) {
	ctx := context.Background()
	daemon := startPlacementTestDaemon(t)
	const scope server.PlacementScope = "deployment"
	provider, err := microvmadapter.NewPlacementProvider(daemon.endpoint(), t.TempDir(), "microvm-local", scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(ctx, Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), UseMock: true,
		PlacementProvider: provider, PlacementScope: scope,
		EnvironmentForkers: map[session.EnvironmentKind]tool.EnvironmentForker{session.EnvironmentKind("microvm"): provider},
		EnvironmentMergers: map[session.EnvironmentKind]tool.EnvironmentMerger{session.EnvironmentKind("microvm"): provider},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.EnvironmentRef.Kind != "microvm" || sess.EnvironmentRef.ID == "" || sess.EnvironmentRef.Revision != "7" {
		t.Fatalf("exact environment ref = %+v", sess.EnvironmentRef)
	}
	if sess.Placement.Kind != "microvm" || sess.Placement.Label != "Local microVM" || sess.Placement.Revision != "7" {
		t.Fatalf("public placement metadata = %+v", sess.Placement)
	}

	run, err := built.Service.StartRunContent(ctx, sess.ID, "continue", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	ops := daemon.operations()
	for _, op := range ops {
		if op == "resolve" || op == "detach" {
			t.Fatalf("daemon operations before CloseSession = %v; run must reuse the create attachment", ops)
		}
	}
	built.Service.CloseSession(sess.ID)
	if got := daemon.operationCount("detach"); got != 1 {
		t.Fatalf("detach operations after CloseSession = %d, want 1", got)
	}
}

func TestMicroVMHarnessContextSelectedRepositoryUsesExactGuestSources(t *testing.T) {
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "same-owner"})
	daemon := startPlacementTestDaemon(t)
	settings := writeOperatorSettingsFile(t, `
execution:
  default_placement: microvm-local
harness_context:
  enabled_sources: [repository]
  kinds:
    instructions: {sources: [repository], mode: combine}
    commands: {sources: [repository], mode: combine}
    rules: {sources: [], mode: combine}
    skills: {sources: [], mode: combine}
    agent_defs: {sources: [], mode: combine}
`)
	manager := &placementReadyManager{endpoint: daemon.endpoint()}
	var requests []port.LLMRequest
	provider := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { requests = append(requests, request) })},
		mockllm.TextTurn("first complete"),
		mockllm.TextTurn("second complete"),
	)
	cfg, err := ConfigureExecution(Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), UseMock: true, MockProvider: provider,
		TrustProject: true, PermissionConfigs: []string{settings}, UserModelDir: t.TempDir(), OwnershipEnforced: true,
		MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microvmmanager.ReadyRequest{}, nil
		},
		MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
			return manager, daemon.endpoint(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	first, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	firstEnvironment := strings.SplitN(first.EnvironmentRef.ID, ".", 2)
	secondEnvironment := strings.SplitN(second.EnvironmentRef.ID, ".", 2)
	if len(firstEnvironment) != 2 || len(secondEnvironment) != 2 {
		t.Fatalf("unexpected refs: first=%+v second=%+v", first.EnvironmentRef, second.EnvironmentRef)
	}
	daemon.writeGuestFile(t, firstEnvironment[1], "AGENTS.md", "FIRST-GUEST-INSTRUCTION")
	daemon.writeGuestFile(t, firstEnvironment[1], ".mecatl/commands/which.md", "FIRST-GUEST-COMMAND")
	daemon.writeGuestFile(t, secondEnvironment[1], "AGENTS.md", "SECOND-GUEST-INSTRUCTION")
	daemon.writeGuestFile(t, secondEnvironment[1], ".mecatl/commands/which.md", "SECOND-GUEST-COMMAND")

	for _, sess := range []*session.Session{first, second} {
		commands, listErr := built.Service.ListCommandsForSession(ctx, sess.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(commands) != 1 || commands[0].Name != "which" {
			t.Fatalf("commands for %s = %+v", sess.ID, commands)
		}
		harnessRun(t, built, ctx, sess.ID, "/which")
	}
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	for index, want := range []struct{ present, absent []string }{
		{present: []string{"FIRST-GUEST-INSTRUCTION", "FIRST-GUEST-COMMAND"}, absent: []string{"SECOND-GUEST-INSTRUCTION", "SECOND-GUEST-COMMAND"}},
		{present: []string{"SECOND-GUEST-INSTRUCTION", "SECOND-GUEST-COMMAND"}, absent: []string{"FIRST-GUEST-INSTRUCTION", "FIRST-GUEST-COMMAND"}},
	} {
		var text strings.Builder
		for _, message := range requests[index].Messages {
			text.WriteString(message.Text)
			text.WriteByte('\n')
		}
		body := text.String()
		for _, present := range want.present {
			if !strings.Contains(body, present) {
				t.Fatalf("request %d source context omitted %q: %q", index, present, body)
			}
		}
		for _, absent := range want.absent {
			if strings.Contains(body, absent) {
				t.Fatalf("request %d source context included other guest %q: %q", index, absent, body)
			}
		}
	}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "other-owner"})
	if _, err := built.Service.ListCommandsForSession(foreign, first.ID); err == nil {
		t.Fatal("foreign owner discovered guest source")
	}
	if got := daemon.operationCount("resolve"); got != 4 {
		t.Fatalf("source borrows = %d, want two exact instruction/command borrows per session", got)
	}
}

func TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact(t *testing.T) {
	ctx := context.Background()
	daemon := startPlacementTestDaemon(t)
	hostSource := t.TempDir()
	storeDir := t.TempDir()
	settings := writeOperatorSettingsFile(t, `execution: {default_placement: microvm-local}`)
	manager := &placementReadyManager{endpoint: daemon.endpoint()}

	config := func(provider *mockllm.Provider) Config {
		configured, err := ConfigureExecution(Config{
			Workspace: hostSource, StoreDir: storeDir, UseMock: true, MockProvider: provider,
			Shell: "/bin/sh", AllowAllTools: true, TrustProject: true, PermissionConfigs: []string{settings},
			MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
				return microvmmanager.ReadyRequest{}, nil
			},
			MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
				return manager, daemon.endpoint(), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return configured
	}

	firstProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("write-a", "Shell", json.RawMessage(`{"command":"printf alpha > a.txt"}`))),
		mockllm.TextTurn("first complete"),
		mockllm.ToolCallTurn(session.NewToolCall("write-b", "Shell", json.RawMessage(`{"command":"test ! -e a.txt && printf beta > b.txt"}`))),
		mockllm.TextTurn("second complete"),
		mockllm.ToolCallTurn(session.NewToolCall("direct-child", "Subagent", json.RawMessage(`{"prompt":"write a direct child marker","mode":"read-write"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("direct-write", "Shell", json.RawMessage(`{"command":"printf direct > direct-child.txt"}`))),
		mockllm.TextTurn("direct child complete"),
		mockllm.TextTurn("parent observed direct child"),
		mockllm.ToolCallTurn(session.NewToolCall("isolated-child", "Subagent", json.RawMessage(`{"prompt":"report your isolated working directory"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("isolated-pwd", "Shell", json.RawMessage(`{"command":"pwd"}`))),
		mockllm.TextTurn("isolated child complete"),
		mockllm.TextTurn("parent observed isolated child"),
	)
	first, err := Build(ctx, config(firstProvider))
	if err != nil {
		t.Fatalf("Build with operator microVM default: %v", err)
	}
	if manager.calls != 0 || daemon.operationCount("create") != 0 {
		first.Close()
		t.Fatalf("composition allocated a microVM: readiness=%d create=%d", manager.calls, daemon.operationCount("create"))
	}

	noFS, err := first.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		first.Close()
		t.Fatalf("create no-fs session: %v", err)
	}
	if noFS.EnvironmentRef.Kind != session.EnvKindNoFS || manager.calls != 0 || daemon.operationCount("create") != 0 {
		first.Close()
		t.Fatalf("no-fs touched microVM state: ref=%+v readiness=%d create=%d", noFS.EnvironmentRef, manager.calls, daemon.operationCount("create"))
	}

	one, err := first.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		first.Close()
		t.Fatalf("create first default session: %v", err)
	}
	two, err := first.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		first.Close()
		t.Fatalf("create second default session: %v", err)
	}
	if manager.calls != 2 || daemon.operationCount("create") != 2 {
		first.Close()
		t.Fatalf("default-session allocation counts: readiness=%d create=%d", manager.calls, daemon.operationCount("create"))
	}
	if got := daemon.operationCount("resolve"); got != 0 {
		first.Close()
		t.Fatalf("unselected repository context acquired guest placement %d times", got)
	}
	if one.EnvironmentRef.Kind != "microvm" || two.EnvironmentRef.Kind != "microvm" || one.EnvironmentRef == two.EnvironmentRef {
		first.Close()
		t.Fatalf("sessions lack distinct exact microVM refs: one=%+v two=%+v", one.EnvironmentRef, two.EnvironmentRef)
	}
	if one.EnvironmentRef.Revision != "7" || two.EnvironmentRef.Revision != "7" || !strings.Contains(one.EnvironmentRef.ID, "logical-1") || !strings.Contains(two.EnvironmentRef.ID, "logical-2") {
		first.Close()
		t.Fatalf("logical worktree identities are not exact/distinct: one=%+v two=%+v", one.EnvironmentRef, two.EnvironmentRef)
	}

	assertSuccessfulRun(t, first.Service, one.ID, "write only in the first guest")
	assertSuccessfulRun(t, first.Service, two.ID, "write only in the second guest")

	forksBefore := daemon.operationCount("fork")
	assertSuccessfulRun(t, first.Service, one.ID, "delegate a direct-write change")
	bindings := daemon.executedBindings()
	if daemon.operationCount("fork") != forksBefore || len(bindings) == 0 || bindings[len(bindings)-1] != "logical-1" {
		first.Close()
		t.Fatalf("direct-write child did not use the parent environment: forks=%d bindings=%v", daemon.operationCount("fork"), bindings)
	}

	assertSuccessfulRun(t, first.Service, one.ID, "delegate isolated inspection")
	bindings = daemon.executedBindings()
	if daemon.operationCount("fork") != forksBefore+1 || len(bindings) == 0 || bindings[len(bindings)-1] == "logical-1" {
		first.Close()
		t.Fatalf("isolated child did not use a distinct logical worktree: forks=%d bindings=%v", daemon.operationCount("fork"), bindings)
	}
	for _, name := range []string{"a.txt", "b.txt", "direct-child.txt"} {
		if _, err := os.Stat(filepath.Join(hostSource, name)); !errors.Is(err, fs.ErrNotExist) {
			first.Close()
			t.Fatalf("guest file %s appeared in host source checkout: %v", name, err)
		}
	}
	first.Close()

	secondProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("read-a", "Shell", json.RawMessage(`{"command":"test \"$(cat a.txt)\" = alpha"}`))),
		mockllm.TextTurn("restart complete"),
	)
	second, err := Build(ctx, config(secondProvider))
	if err != nil {
		t.Fatalf("restart app composition: %v", err)
	}
	if daemon.operationCount("create") != 2 {
		second.Close()
		t.Fatalf("restart replaced an existing placement: create=%d", daemon.operationCount("create"))
	}
	assertSuccessfulRun(t, second.Service, one.ID, "verify the prior guest change")
	if daemon.operationCount("resolve") == 0 {
		second.Close()
		t.Fatal("restart run did not reattach the exact persisted ref")
	}
	second.Close()

	daemon.loseRuntimeState()
	third, err := Build(ctx, config(mockllm.New(mockllm.TextTurn("must not run"))))
	if err != nil {
		t.Fatalf("build after simulated microvmd restart: %v", err)
	}
	defer third.Close()
	createsBefore := daemon.operationCount("create")
	if _, err := third.Service.StartRunContent(ctx, one.ID, "must fail closed", nil); err == nil {
		t.Fatal("lost microvmd generation silently fell back to host-local")
	}
	if daemon.operationCount("create") != createsBefore {
		t.Fatalf("lost generation was replaced: create before=%d after=%d", createsBefore, daemon.operationCount("create"))
	}
	preserved, err := third.Service.GetSession(ctx, one.ID)
	if err != nil || preserved.EnvironmentRef != one.EnvironmentRef {
		t.Fatalf("failed reattach did not preserve exact state: session=%+v err=%v", preserved, err)
	}
}

func TestHostLocalOmissionDoesNoMicroVMWork(t *testing.T) {
	factoryCalled := false
	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), UseMock: true,
		MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microvmmanager.ReadyRequest{}, nil
		},
		MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
			factoryCalled = true
			return nil, "", errors.New("must not be called")
		},
	})
	if err != nil {
		t.Fatalf("host-local Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("host-local CreateSession: %v", err)
	}
	if factoryCalled || sess.EnvironmentRef.Kind != session.EnvKindLocal {
		t.Fatalf("omitted placement did MicroVM work or selected wrong backend: factory=%v ref=%+v", factoryCalled, sess.EnvironmentRef)
	}
}

func TestMicroVMScheduledPlacementSurvivesFiresRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	daemon := startPlacementTestDaemon(t)
	storeDir := t.TempDir()
	workspace := t.TempDir()
	const scope server.PlacementScope = "deployment"

	build := func(llm *mockllm.Provider) *Built {
		t.Helper()
		provider, err := microvmadapter.NewPlacementProvider(daemon.endpoint(), workspace, "microvm-local", scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		built, err := Build(ctx, Config{
			Workspace: workspace, StoreDir: storeDir, UseMock: true, MockProvider: llm,
			Shell: "/bin/sh", AllowAllTools: true, TrustProject: true,
			PlacementProvider: provider, PlacementScope: scope,
			EnvironmentForkers: map[session.EnvironmentKind]tool.EnvironmentForker{session.EnvironmentKind("microvm"): provider},
			EnvironmentMergers: map[session.EnvironmentKind]tool.EnvironmentMerger{session.EnvironmentKind("microvm"): provider},
			SchedulerEnabled:   true, SchedulerTickInterval: time.Hour, SessionLeaseTTL: 200 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return built
	}

	first := build(mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("write-first", "Shell", json.RawMessage(`{"command":"printf first > shared.txt"}`))),
		mockllm.TextTurn("first fire complete"),
	))
	origin, err := first.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	borrowed, err := first.Service.CreateSchedule(ctx, port.ScheduleSpec{Name: "borrowed", Prompt: "unused", Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, OriginSessionID: origin.ID, Mutating: true})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if borrowed.Spec.PlacementOwned || borrowed.Spec.EnvironmentRef != origin.EnvironmentRef {
		first.Close()
		t.Fatalf("borrowed placement = %+v", borrowed.Spec)
	}
	neverFired, err := first.Service.CreateSchedule(ctx, port.ScheduleSpec{Name: "never-fired", Prompt: "unused", Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, Mutating: true})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if !neverFired.Spec.PlacementOwned || daemon.operationCount("create") != 2 {
		first.Close()
		t.Fatalf("never-fired independent allocation = owned:%v creates:%d", neverFired.Spec.PlacementOwned, daemon.operationCount("create"))
	}
	parts := strings.SplitN(neverFired.Spec.EnvironmentRef.ID, ".", 2)
	if len(parts) != 2 {
		first.Close()
		t.Fatalf("invalid never-fired ref: %+v", neverFired.Spec.EnvironmentRef)
	}
	if err := first.Service.DeleteSchedule(ctx, neverFired.Spec.Name); err != nil {
		first.Close()
		t.Fatal(err)
	}
	wantDeleteClaim := fmt.Sprintf("local/%s/%s/%s@%s/%s", parts[0], parts[1], parts[1], neverFired.Spec.EnvironmentRef.Revision, neverFired.Spec.EnvironmentRef.Revision)
	if claims := daemon.deleteClaimsSnapshot(); len(claims) != 1 || claims[0] != wantDeleteClaim {
		first.Close()
		t.Fatalf("never-fired delete binding = %v, want [%s]", claims, wantDeleteClaim)
	}
	if _, err := os.Stat(filepath.Join(daemon.root, parts[1])); !errors.Is(err, fs.ErrNotExist) {
		first.Close()
		t.Fatalf("never-fired worktree survived daemon cleanup: %v", err)
	}
	if _, err := first.Service.GetSchedule(ctx, neverFired.Spec.Name); !errors.Is(err, port.ErrScheduleNotFound) {
		first.Close()
		t.Fatalf("never-fired schedule survived cleanup: %v", err)
	}
	if _, err := first.Service.GetSession(ctx, origin.ID); err != nil {
		first.Close()
		t.Fatalf("never-fired cleanup tore down borrowed origin: %v", err)
	}
	created, err := first.Service.CreateSchedule(ctx, port.ScheduleSpec{Name: "durable", Prompt: "maintain shared marker", Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, Mutating: true})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if !created.Spec.PlacementOwned || daemon.operationCount("create") != 3 {
		first.Close()
		t.Fatalf("independent allocation = owned:%v creates:%d", created.Spec.PlacementOwned, daemon.operationCount("create"))
	}
	fire1, err := first.Service.FireNow(ctx, "durable")
	if err != nil || fire1.Stop != session.StopEndTurn {
		first.Close()
		t.Fatalf("first FireNow = %+v, %v", fire1, err)
	}
	first.Close()

	second := build(mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("verify-second", "Shell", json.RawMessage(`{"command":"test \"$(cat shared.txt)\" = first && printf second >> shared.txt"}`))),
		mockllm.TextTurn("second fire complete"),
		mockllm.ToolCallTurn(session.NewToolCall("verify-resume", "Shell", json.RawMessage(`{"command":"test \"$(cat shared.txt)\" = firstsecond"}`))),
		mockllm.TextTurn("historic fire resumed"),
	))
	defer second.Close()
	loaded, err := second.Service.GetSchedule(ctx, "durable")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Spec.EnvironmentRef != created.Spec.EnvironmentRef || !loaded.Spec.PlacementOwned || daemon.operationCount("create") != 3 {
		t.Fatalf("restart rebound placement: ref=%+v owned=%v creates=%d", loaded.Spec.EnvironmentRef, loaded.Spec.PlacementOwned, daemon.operationCount("create"))
	}
	var fire2 port.ScheduleFire
	if !eventually(5*time.Second, func() bool {
		fire2, err = second.Service.FireNow(ctx, "durable")
		return err == nil
	}) || fire2.Stop != session.StopEndTurn {
		t.Fatalf("second FireNow = %+v, %v", fire2, err)
	}
	if err := second.Service.DeleteSchedule(ctx, "borrowed"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Service.GetSession(ctx, origin.ID); err != nil {
		t.Fatalf("borrowed-origin session lost: %v", err)
	}
	detaches := daemon.operationCount("detach")
	if err := second.Service.DeleteSchedule(ctx, "durable"); err != nil {
		t.Fatal(err)
	}
	if daemon.operationCount("detach") != detaches {
		t.Fatal("post-claim schedule deletion destroyed its placement")
	}
	run, err := second.Service.StartScheduledRunContent(ctx, fire2.SessionID, "resume after schedule deletion", nil)
	if err != nil {
		t.Fatalf("resume historical fire session: %v", err)
	}
	var result *session.ResultPayload
	for event := range run.Events() {
		if event.Result != nil {
			result = event.Result
		}
		if event.ToolResult != nil && event.ToolResult.IsError {
			t.Fatalf("resumed historical fire tool failed: %s", event.ToolResult.Content)
		}
	}
	if result == nil || result.Stop == session.StopError {
		t.Fatalf("resumed historical fire result = %+v", result)
	}
}

func assertSuccessfulRun(t *testing.T, svc *server.Service, id session.SessionID, prompt string) {
	t.Helper()
	run, err := svc.StartRunContent(context.Background(), id, prompt, nil)
	if err != nil {
		t.Fatalf("StartRunContent(%s): %v", id, err)
	}
	var result *session.ResultPayload
	for event := range run.Events() {
		if event.Result != nil {
			result = event.Result
		}
		if event.ToolResult != nil && event.ToolResult.IsError {
			t.Fatalf("tool %s failed: %s", event.ToolResult.CallID, event.ToolResult.Content)
		}
	}
	if result == nil || result.Stop == session.StopError {
		t.Fatalf("run result = %+v", result)
	}
}

func readPlacementFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}

func writePlacementFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	frame.Write(header[:])
	frame.Write(payload)
	_, err = w.Write(frame.Bytes())
	return err
}
