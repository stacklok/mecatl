package executionexecutor

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

const failAfter = 10 * time.Second

// faultConn is the client end of a connection that a test drops or blackholes.
type faultConn struct {
	net.Conn
	blackholed atomic.Bool
}

func (c *faultConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.blackholed.Load() {
			return n, err
		}
	}
}

func (c *faultConn) Write(p []byte) (int, error) {
	if c.blackholed.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

// dialFaulty serves srv with the executor's keepalive and returns a client
// whose first connection is sent on the returned channel.
func dialFaulty(
	t *testing.T,
	srv executorv1.CommandServiceServer,
	interval time.Duration,
	timeout time.Duration,
) (executorv1.CommandServiceClient, <-chan *faultConn) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	keepalive := executionenv.ExecutorKeepalive{Interval: interval, Timeout: timeout}
	gs := grpc.NewServer(ServerKeepalive(keepalive)...)
	executorv1.RegisterCommandServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conns := make(chan *faultConn, 1)
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			raw, err := lis.DialContext(ctx)
			if err != nil {
				return nil, err
			}
			fc := &faultConn{Conn: raw}
			select {
			case conns <- fc:
			default:
			}
			return fc, nil
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return executorv1.NewCommandServiceClient(conn), conns
}

func startCommand(t *testing.T, client executorv1.CommandServiceClient, command string) {
	t.Helper()
	stream, err := client.RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: command},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// blockingRunner runs commands that end only when their context does.
type blockingRunner struct {
	started   chan struct{}
	cancelled chan time.Time
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 1), cancelled: make(chan time.Time, 1)}
}

func (r *blockingRunner) Start(
	ctx context.Context,
	_ string,
	_ io.Writer,
	_ io.Writer,
) (commandserver.Process, error) {
	r.started <- struct{}{}
	return blockedProcess{ctx: ctx, cancelled: r.cancelled}, nil
}

type blockedProcess struct {
	ctx       context.Context
	cancelled chan<- time.Time
}

func (p blockedProcess) Wait() commandserver.Exit {
	<-p.ctx.Done()
	p.cancelled <- time.Now()
	return commandserver.Exit{Code: -1, Confirmed: true}
}

func runBlocked(t *testing.T, interval, timeout time.Duration) (*blockingRunner, *faultConn) {
	t.Helper()
	runner := newBlockingRunner()
	srv, err := commandserver.New(runner)
	if err != nil {
		t.Fatal(err)
	}
	client, conns := dialFaulty(t, srv, interval, timeout)
	startCommand(t, client, "block")
	select {
	case <-runner.started:
	case <-time.After(failAfter):
		t.Fatal("command did not start")
	}
	return runner, <-conns
}

func TestDroppedConnectionCancelsCommand(t *testing.T) {
	runner, conn := runBlocked(t, time.Hour, time.Hour)
	_ = conn.Close()
	select {
	case <-runner.cancelled:
	case <-time.After(failAfter):
		t.Fatal("command outlived its dropped connection")
	}
}

func TestBlackholedClientIsDetectedWithinKeepalive(t *testing.T) {
	const interval, timeout, slack = time.Second, time.Second, 2 * time.Second
	runner, conn := runBlocked(t, interval, timeout)
	conn.blackholed.Store(true)
	since := time.Now()
	select {
	case at := <-runner.cancelled:
		if elapsed := at.Sub(since); elapsed > interval+timeout+slack {
			t.Fatalf("cancelled after %s, want at most %s", elapsed, interval+timeout+slack)
		}
	case <-time.After(failAfter):
		t.Fatal("blackholed client was never detected")
	}
}
