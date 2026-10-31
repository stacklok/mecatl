//go:build unix

package customization

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStatusLine_Scenario4_ProcessLifecycleAndGenerationGuard(t *testing.T) {
	dir := t.TempDir()
	openFIFO := func(name string, flags int) *os.File {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, flags|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	child := openFIFO("child", os.O_RDONLY)
	control := openFIFO("control", os.O_RDWR)
	// Keep the read end from seeing EOF before the descendant opens its writer.
	keepalive, err := os.OpenFile(child.Name(), os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keepalive.Close() })

	source := NewCommandSource(commandTest(dir, "latest", child.Name(), control.Name()))
	t.Cleanup(func() {
		// Release even an orphaned descendant if containment is broken.
		_, _ = control.WriteString("release\n")
		if err := source.Close(context.Background()); err != nil {
			t.Errorf("close command source: %v", err)
		}
	})
	status := source.(commandSource).status
	// Replacement must cancel the command; its normal deadline must not rescue it.
	status.invocationTimeout = 0
	firstContext := make(chan context.Context, 1)
	render := status.render
	status.render = func(ctx context.Context, input Input) renderedStatusLine {
		if input.Session.Title == "first" {
			firstContext <- ctx
		}
		return render(ctx, input)
	}

	source.Submit(Input{Session: Session{Title: "first"}, Terminal: Terminal{FooterAvailCols: 80}})
	if err := child.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(child, ready[:]); err != nil || ready[0] != 'x' {
		t.Fatalf("descendant did not announce startup: byte=%q err=%v", ready, err)
	}
	if err := keepalive.Close(); err != nil {
		t.Fatal(err)
	}
	firstCtx := <-firstContext
	source.Submit(Input{Session: Session{Title: "latest"}, Terminal: Terminal{FooterAvailCols: 80}})
	select {
	case <-firstCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not cancel the older command")
	}
	select {
	case <-source.Changed():
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not cancel and join the older command")
	}
	if got := statusSurfaceText(source.Latest().Footer); got != "latest" {
		t.Fatalf("stale command completion won: footer = %q", got)
	}
	// Only the descendant owns the remaining writer. WaitDelay can release the
	// command's stdout reader, but cannot close this independent pipe for it.
	if err := child.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := child.Read(ready[:]); n != 0 || err != io.EOF {
		t.Fatalf("descendant retained its pipe after cancellation: n=%d err=%v", n, err)
	}
}
