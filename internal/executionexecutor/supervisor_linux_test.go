//go:build linux

package executionexecutor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executorrpc/commandconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

const (
	helperEnv = "MECATL_SUPERVISOR_TEST_HELPER"
	spareEnv  = "MECATL_SUPERVISOR_TEST_SPARE"
	sweepEnv  = "MECATL_SUPERVISOR_TEST_SWEEP"
)

// token is a sleep duration of whole seconds whose fraction marks the process.
func token(seconds int) string {
	return fmt.Sprintf("%d.%09d", seconds, time.Now().UnixNano()%1_000_000_000)
}

func supervisorClient(t *testing.T, root, spare string) executorv1.CommandServiceClient {
	t.Helper()
	supervisor := NewCommandSupervisor(
		[]string{os.Args[0], "-test.run=^TestCommandSupervisorHelper$"},
		[]string{helperEnv + "=1", spareEnv + "=" + spare, "MECATL_LEAK_SENTINEL=leaked"},
		root)
	return commandconformance.Dial(t, commandserver.New(supervisor, commandserver.Options{}))
}

// processesWith lists live processes whose command line contains marker.
func processesWith(marker string) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if bytes.Contains(cmdline, []byte(marker)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func TestCommandSupervisorConformance(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is required to escape the command's process group")
	}
	spare, child := token(5), token(3600)
	client := supervisorClient(t, t.TempDir(), spare)
	quiet := "</dev/null >/dev/null 2>&1"
	commandconformance.Run(t, commandconformance.Harness{
		Client:         func(*testing.T) executorv1.CommandServiceClient { return client },
		Succeed:        "printf out; printf err >&2",
		Fail:           "exit 3",
		Block:          "printf ready; exec sleep 3600",
		BlockWithChild: "setsid sleep " + child + " " + quiet + " & printf ready; wait",
		ChildGone: func(ctx context.Context) error {
			for len(processesWith(child)) > 0 {
				select {
				case <-ctx.Done():
					return fmt.Errorf("still running: %v", processesWith(child))
				case <-time.After(10 * time.Millisecond):
				}
			}
			return nil
		},
		Survivor: "setsid sh -c ': > spared; exec sleep " + spare + "' " + quiet +
			" & while [ ! -e spared ]; do sleep 0.01; done",
	})
}

func runCommand(
	t *testing.T, client executorv1.CommandServiceClient, command string,
) (string, *executorv1.CommandResult) {
	t.Helper()
	stream, err := client.RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: command},
	}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out.Write(msg.GetStdout())
		if msg.GetResult() != nil {
			return out.String(), msg.GetResult()
		}
	}
}

func TestCommandSupervisorKeepsMinimalEnvironmentAndIdentity(t *testing.T) {
	root := t.TempDir()
	client := supervisorClient(t, root, "")
	out, res := runCommand(t, client, "env; echo uid=$(id -u); echo pwd=$(pwd -P)")
	if res.GetState() != executorv1.CommandState_COMMAND_STATE_SUCCEEDED {
		t.Fatalf("result %v: %s", res, out)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"HOME=/workspace\n", "TMPDIR=/tmp\n",
		fmt.Sprintf("uid=%d\n", os.Getuid()), "pwd=" + resolved + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MECATL_") {
		t.Fatalf("helper environment leaked into the command:\n%s", out)
	}
}

func TestCommandSupervisorIsolatesConcurrentCommands(t *testing.T) {
	client := supervisorClient(t, t.TempDir(), "")
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := client.RunCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: "(sleep 3600 &); printf ready; exec sleep 3600"},
	}}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(msg.GetStdout(), []byte("ready")) {
			break
		}
	}
	time.AfterFunc(100*time.Millisecond, cancel)
	out, res := runCommand(t, client, "(sleep 0.5 &); sleep 0.3; printf done")
	if res.GetState() != executorv1.CommandState_COMMAND_STATE_SUCCEEDED || out != "done" {
		t.Fatalf("concurrent command = %v %q", res, out)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("cancelled command kept streaming")
	}
}

func TestOrphanReaperSparesHelpers(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommandSupervisorHelper$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), sweepEnv + "=" + token(0)[2:]}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sweep helper: %v\n%s", err, out)
	}
}

// TestCommandSupervisorHelper is the supervisor's helper process, or the
// orphan reaper's subreaper process, when the environment selects one.
func TestCommandSupervisorHelper(_ *testing.T) {
	if marker := os.Getenv(sweepEnv); marker != "" {
		os.Exit(sweep(marker))
	}
	if os.Getenv(helperEnv) != "1" {
		return
	}
	spare := os.Getenv(spareEnv)
	killChild = func(pid int) {
		cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if spare == "" || !bytes.Contains(cmdline, []byte(spare)) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	status := os.NewFile(3, "status")
	if err := SuperviseCommand(os.Stdin, status, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func sweep(marker string) int {
	if err := EnableCommandExecution(); err != nil {
		return 2
	}
	s := NewCommandSupervisor(nil, nil, "")
	helper := exec.Command("sleep", "60."+marker)
	if err := helper.Start(); err != nil {
		return 3
	}
	s.helpers[helper.Process.Pid] = struct{}{}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	if err := exec.Command("/bin/sh", "-c", "sleep 30."+marker+" &").Run(); err != nil {
		return 4
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go s.ReapOrphans(ctx, 10*time.Millisecond)
	for {
		if pids := processesWith("30." + marker); len(pids) == 0 {
			break
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 5
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(processesWith("60."+marker)) != 1 {
		return 6
	}
	return 0
}
