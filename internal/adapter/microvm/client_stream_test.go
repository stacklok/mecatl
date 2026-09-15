package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

const wireTestTimeout = 5 * time.Second

func TestRunnerManagedTemporaryScopeCrossesOnlyAsClosedCapability(t *testing.T) {
	client, claim, stop := startExecWireServer(t, func(conn net.Conn) {
		var request lifecycleRequest
		if err := readFrame(conn, &request); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var input struct {
			Command        string              `json:"command"`
			TemporaryScope tool.TemporaryScope `json:"temporary_scope"`
		}
		if err := json.Unmarshal(request.Payload, &input); err != nil {
			t.Errorf("decode exec payload: %v", err)
			return
		}
		if input.Command != "pwd" || input.TemporaryScope != tool.TemporaryScopeManaged {
			t.Errorf("exec payload = %+v", input)
			return
		}
		_ = writeFrame(conn, lifecycleResponse{Payload: mustJSON(t, execResponse{ExitCode: 0})})
	})
	defer stop()

	result, err := (&runner{client: client, binding: claim}).RunWithTemporaryScope(context.Background(), "pwd", tool.TemporaryScopeManaged)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("RunWithTemporaryScope = %+v, %v", result, err)
	}
}

func TestRunnerStreamingPreservesDaemonFrameOrderAndNonzeroExit(t *testing.T) {
	client, claim, stop := startExecWireServer(t, func(conn net.Conn) {
		var request lifecycleRequest
		if err := readFrame(conn, &request); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		for _, frame := range []lifecycleResponse{
			{Stream: &execStreamFrame{Channel: "stdout", Data: []byte("out-1\n")}},
			{Stream: &execStreamFrame{Channel: "stderr", Data: []byte("err-1\n")}},
			{Stream: &execStreamFrame{Channel: "stdout", Data: []byte("out-2\n")}},
			{Payload: mustJSON(t, execResponse{ExitCode: 9})},
		} {
			if err := writeFrame(conn, frame); err != nil {
				t.Errorf("write response: %v", err)
				return
			}
		}
	})
	defer stop()

	var output bytes.Buffer
	exit, err := (&runner{client: client, binding: claim}).RunStreaming(context.Background(), "ignored", &output)
	if err != nil || exit != 9 || output.String() != "out-1\nerr-1\nout-2\n" {
		t.Fatalf("RunStreaming = output %q, exit %d, err %v", output.String(), exit, err)
	}
}

func TestRunnerCancellationWithoutDeadlineClosesRequestAndPreservesOutput(t *testing.T) {
	peerClosed := make(chan struct{})
	client, claim, stop := startExecWireServer(t, func(conn net.Conn) {
		var request lifecycleRequest
		if err := readFrame(conn, &request); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := writeFrame(conn, lifecycleResponse{Stream: &execStreamFrame{Channel: "stdout", Data: []byte("partial\n")}}); err != nil {
			t.Errorf("write partial output: %v", err)
			return
		}
		var one [1]byte
		_, _ = conn.Read(one[:])
		close(peerClosed)
	})
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputSeen := make(chan struct{})
	resultCh := make(chan struct {
		result string
		err    error
	}, 1)
	go func() {
		var output notifyingBuffer
		output.notify = outputSeen
		_, err := (&runner{client: client, binding: claim}).RunStreaming(ctx, "ignored", &output)
		resultCh <- struct {
			result string
			err    error
		}{output.String(), err}
	}()
	select {
	case <-outputSeen:
		cancel()
	case <-time.After(wireTestTimeout):
		t.Fatal("partial output was not delivered")
	}
	select {
	case got := <-resultCh:
		if !errors.Is(got.err, context.Canceled) || got.result != "partial\n" {
			t.Fatalf("Run cancellation = output %q, err %v", got.result, got.err)
		}
	case <-time.After(wireTestTimeout):
		t.Fatal("request cancellation did not unblock a deadline-free read")
	}
	select {
	case <-peerClosed:
	case <-time.After(wireTestTimeout):
		t.Fatal("client cancellation did not close the peer request")
	}
}

func TestRunnerTransportFailureIsDistinctAndPreservesPriorOutput(t *testing.T) {
	client, claim, stop := startExecWireServer(t, func(conn net.Conn) {
		var request lifecycleRequest
		if err := readFrame(conn, &request); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		_ = writeFrame(conn, lifecycleResponse{Stream: &execStreamFrame{Channel: "stderr", Data: []byte("before-disconnect\n")}})
	})
	defer stop()

	result, err := (&runner{client: client, binding: claim}).Run(context.Background(), "ignored")
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport error = %v, want non-context failure", err)
	}
	if result.Stderr != "before-disconnect\n" {
		t.Fatalf("prior stderr = %q", result.Stderr)
	}
}

type notifyingBuffer struct {
	bytes.Buffer
	notify chan struct{}
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	n, err := b.Buffer.Write(p)
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return n, err
}

func startExecWireServer(t *testing.T, serve func(net.Conn)) (*Client, binding, func()) {
	t.Helper()
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		serve(conn)
	}()
	client, err := New("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	claim := binding{Owner: "local", SessionID: "session-1", EnvironmentID: "env-1", Ref: "env-1@1", Generation: 1}
	return client, claim, func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(wireTestTimeout):
			t.Error("wire server did not stop")
		}
	}
}
