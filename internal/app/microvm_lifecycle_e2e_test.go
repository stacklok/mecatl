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

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementTestDaemon struct {
	listener     net.Listener
	root         string
	mu           sync.Mutex
	ops          []string
	next         int
	guests       map[string]string
	execBindings []string
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
	d := &placementTestDaemon{listener: listener, root: t.TempDir(), guests: make(map[string]string)}
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
		environmentID := fmt.Sprintf("worktree-%d", d.next)
		guestRoot := filepath.Join(d.root, environmentID)
		d.guests[environmentID] = guestRoot
		d.mu.Unlock()
		if err := os.MkdirAll(guestRoot, 0o700); err != nil {
			response = map[string]any{"error_code": "create_failed", "error": "guest root unavailable"}
			break
		}
		binding = map[string]any{"owner": "local", "session_id": sessionID, "environment_id": environmentID, "ref": environmentID + "@7", "generation": 7}
		response["binding"] = binding
		response["created"] = map[string]any{
			"ref": map[string]any{"Kind": "microvm", "ID": environmentID + "@7"}, "generation": 7,
			"host_worktree": guestRoot, "guest_root": "/workspace", "profile": "microvm-local",
			"guest_egress": "deny-all", "host_egress": "not constrained",
		}
	case "resolve", "detach":
		if !d.hasBinding(binding) {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
		}
	case "fork":
		parentRoot, ok := d.guestRoot(binding)
		if !ok {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
			break
		}
		d.mu.Lock()
		d.next++
		childID := fmt.Sprintf("worktree-%d", d.next)
		childRoot := filepath.Join(d.root, childID)
		d.guests[childID] = childRoot
		d.mu.Unlock()
		if err := copyPlacementTree(parentRoot, childRoot); err != nil {
			response = map[string]any{"error_code": "fork_failed", "error": "child root unavailable"}
			break
		}
		response["binding"] = map[string]any{
			"owner": binding["owner"], "session_id": binding["session_id"],
			"environment_id": childID, "ref": childID + "@7", "generation": 7,
		}
	case "child-delete":
		if !d.hasBinding(binding) {
			response = map[string]any{"error_code": "not_found", "error": "generation unavailable"}
		}
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
	_ = writePlacementFrame(conn, response)
}

func (d *placementTestDaemon) guestRoot(binding map[string]any) (string, bool) {
	environmentID, _ := binding["environment_id"].(string)
	d.mu.Lock()
	defer d.mu.Unlock()
	root, ok := d.guests[environmentID]
	return root, ok
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

func TestMicroVMDefaultPlacementUsesNormalCreateSessionAndExactReattach(t *testing.T) {
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
	ops := daemon.operations()
	foundResolve := false
	for _, op := range ops {
		foundResolve = foundResolve || op == "resolve"
	}
	if !foundResolve {
		t.Fatalf("daemon operations = %v; exact reattach was not exercised", ops)
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
		return Config{
			Workspace: hostSource, StoreDir: storeDir, UseMock: true, MockProvider: provider,
			Shell: "/bin/sh", AllowAllTools: true, TrustProject: true, PermissionConfigs: []string{settings},
			MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
				return microvmmanager.ReadyRequest{}, nil
			},
			MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
				return manager, daemon.endpoint(), nil
			},
		}
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
	if one.EnvironmentRef.Kind != "microvm" || two.EnvironmentRef.Kind != "microvm" || one.EnvironmentRef == two.EnvironmentRef {
		first.Close()
		t.Fatalf("sessions lack distinct exact microVM refs: one=%+v two=%+v", one.EnvironmentRef, two.EnvironmentRef)
	}
	if one.EnvironmentRef.Revision != "7" || two.EnvironmentRef.Revision != "7" || !strings.Contains(one.EnvironmentRef.ID, "worktree-1") || !strings.Contains(two.EnvironmentRef.ID, "worktree-2") {
		first.Close()
		t.Fatalf("logical worktree identities are not exact/distinct: one=%+v two=%+v", one.EnvironmentRef, two.EnvironmentRef)
	}

	assertSuccessfulRun(t, first.Service, one.ID, "write only in the first guest")
	assertSuccessfulRun(t, first.Service, two.ID, "write only in the second guest")

	forksBefore := daemon.operationCount("fork")
	assertSuccessfulRun(t, first.Service, one.ID, "delegate a direct-write change")
	bindings := daemon.executedBindings()
	if daemon.operationCount("fork") != forksBefore || len(bindings) == 0 || bindings[len(bindings)-1] != "worktree-1" {
		first.Close()
		t.Fatalf("direct-write child did not use the parent environment: forks=%d bindings=%v", daemon.operationCount("fork"), bindings)
	}

	assertSuccessfulRun(t, first.Service, one.ID, "delegate isolated inspection")
	bindings = daemon.executedBindings()
	if daemon.operationCount("fork") != forksBefore+1 || len(bindings) == 0 || bindings[len(bindings)-1] == "worktree-1" {
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
