//go:build linux

package executionexecutor

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

const (
	helperStartTimeout = 10 * time.Second
	helperStopTimeout  = descendantCleanupTimeout + 3*time.Second
	orphanDrainTimeout = time.Second
	copyBufferBytes    = 32 << 10
)

// SuperviseCommand is the per-command helper process. It reads a
// length-prefixed command from control, runs it with output on stdout and
// stderr, and stops it when control yields another byte or ends. It writes
// "started" and then "exit <code> <confirmed>" lines to status.
func SuperviseCommand(control io.Reader, status io.Writer, stdout, stderr *os.File) error {
	if err := EnableCommandExecution(); err != nil {
		return err
	}
	var size [4]byte
	if _, err := io.ReadFull(control, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > executionenv.MaxCommandBytes {
		return errors.New("invalid command size")
	}
	command := make([]byte, n)
	if _, err := io.ReadFull(control, command); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var one [1]byte
		_, _ = control.Read(one[:])
		cancel()
	}()
	started := false
	code, proven, err := runShell(ctx, "", string(command), stdout, stderr, func() {
		started = true
		_, _ = io.WriteString(status, "started\n")
	})
	if !started {
		return err
	}
	_ = stdout.Close()
	_ = stderr.Close()
	_, err = fmt.Fprintf(status, "exit %d %t\n", code, proven)
	return err
}

// CommandSupervisor runs each command under its own helper process, a
// subreaper, so every process a command starts stays attributable to it while
// other commands run.
type CommandSupervisor struct {
	helper  []string
	env     []string
	dir     string
	mu      sync.Mutex
	helpers map[int]struct{}
}

// NewCommandSupervisor runs helper, an argv whose process calls
// SuperviseCommand, with env in dir.
func NewCommandSupervisor(helper, env []string, dir string) *CommandSupervisor {
	return &CommandSupervisor{helper: helper, env: env, dir: dir, helpers: make(map[int]struct{})}
}

type supervised struct {
	s        *CommandSupervisor
	ctx      context.Context
	cmd      *exec.Cmd
	ctrl     *os.File
	statusR  *os.File
	status   *bufio.Reader
	outputs  []*os.File
	copied   sync.WaitGroup
	finished chan struct{}
}

// Start implements commandserver.Runner.
func (s *CommandSupervisor) Start(
	ctx context.Context, command string, stdout, stderr io.Writer,
) (commandserver.Process, error) {
	if len(command) == 0 || len(command) > executionenv.MaxCommandBytes {
		return nil, errors.New("command size out of bounds")
	}
	var parent, child []*os.File
	closeAll := func(files []*os.File) {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for i := 0; i < 4; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			closeAll(parent)
			closeAll(child)
			return nil, err
		}
		if i == 0 {
			r, w = w, r
		}
		parent, child = append(parent, r), append(child, w)
	}
	cmd := &exec.Cmd{
		Path: s.helper[0], Args: s.helper, Env: s.env, Dir: s.dir,
		Stdin: child[0], Stdout: child[1], Stderr: child[2], ExtraFiles: child[3:],
	}
	s.mu.Lock()
	err := cmd.Start()
	if err == nil {
		s.helpers[cmd.Process.Pid] = struct{}{}
	}
	s.mu.Unlock()
	closeAll(child)
	if err != nil {
		closeAll(parent)
		return nil, err
	}
	p := &supervised{
		s: s, ctx: ctx, cmd: cmd, ctrl: parent[0], outputs: parent[1:3],
		statusR: parent[3], status: bufio.NewReader(parent[3]), finished: make(chan struct{}),
	}
	if err := p.handshake(command); err != nil {
		close(p.finished)
		_ = cmd.Process.Kill()
		p.release()
		return nil, err
	}
	p.copied.Add(2)
	go p.copy(p.outputs[0], stdout)
	go p.copy(p.outputs[1], stderr)
	go func() {
		select {
		case <-ctx.Done():
			_, _ = p.ctrl.Write([]byte{1})
		case <-p.finished:
		}
	}()
	return p, nil
}

func (p *supervised) handshake(command string) error {
	msg := binary.BigEndian.AppendUint32(nil, uint32(len(command))) //nolint:gosec // Bounded above.
	if _, err := p.ctrl.Write(append(msg, command...)); err != nil {
		return err
	}
	_ = p.statusR.SetReadDeadline(time.Now().Add(helperStartTimeout))
	line, err := p.status.ReadString('\n')
	if err != nil || line != "started\n" {
		return errors.New("command did not start")
	}
	return p.statusR.SetReadDeadline(time.Time{})
}

// Wait implements commandserver.Process. Without a confirmed exit it kills
// the helper; any survivors then reach this process's orphan reaper.
func (p *supervised) Wait() commandserver.Exit {
	lines := make(chan string, 1)
	go func() {
		line, _ := p.status.ReadString('\n')
		lines <- line
	}()
	var line string
	select {
	case line = <-lines:
	case <-p.ctx.Done():
		select {
		case line = <-lines:
		case <-time.After(helperStopTimeout):
		}
	}
	close(p.finished)
	exit := commandserver.Exit{Code: -1}
	if _, err := fmt.Sscanf(line, "exit %d %t\n", &exit.Code, &exit.Confirmed); err != nil {
		exit = commandserver.Exit{Code: -1}
	}
	if !exit.Confirmed {
		_ = p.cmd.Process.Kill()
		for _, f := range p.outputs {
			_ = f.SetReadDeadline(time.Now().Add(orphanDrainTimeout))
		}
	}
	p.copied.Wait()
	p.release()
	return exit
}

// release reaps the helper and closes the parent's pipe ends.
func (p *supervised) release() {
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(helperStopTimeout):
		_ = p.cmd.Process.Kill()
		<-done
	}
	p.s.mu.Lock()
	delete(p.s.helpers, p.cmd.Process.Pid)
	p.s.mu.Unlock()
	for _, f := range append([]*os.File{p.ctrl, p.statusR}, p.outputs...) {
		_ = f.Close()
	}
}

// copy forwards output until the pipe ends, discarding it once w fails so
// the command never blocks on a full pipe.
func (p *supervised) copy(r io.Reader, w io.Writer) {
	defer p.copied.Done()
	buf := make([]byte, copyBufferBytes)
	failed := false
	for {
		n, err := r.Read(buf)
		if n > 0 && !failed {
			_, werr := w.Write(buf[:n])
			failed = werr != nil
		}
		if err != nil {
			return
		}
	}
}

// ReapOrphans kills and reaps, every interval until ctx ends, each child of
// this process that is not a helper. Orphans arrive here when a helper is
// killed and this process is a subreaper or PID 1.
func (s *CommandSupervisor) ReapOrphans(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.reapOrphans()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *CommandSupervisor) reapOrphans() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pid := range directChildren() {
		if _, ok := s.helpers[pid]; ok {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	}
}
