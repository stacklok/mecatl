package boatenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// sandbox is the one handle a binding's Workspace and CommandRunner share.
// It makes the sandbox ready lazily: reattaching a persisted ref costs a
// single GET, and the resume (plus workdir creation) happens on the first
// operation that actually needs the guest. That keeps server paths that only
// need the ref or Root() from resuming, and billing for, an archived sandbox.
type sandbox struct {
	client       *apiClient
	id           string
	workdir      string
	ttlSeconds   int
	readyTimeout time.Duration

	mu    sync.Mutex
	ready bool
}

func newSandbox(p *Provider, id string, ready bool) *sandbox {
	return &sandbox{client: p.client, id: id, workdir: p.workdir, ttlSeconds: p.ttlSeconds, readyTimeout: p.readyTimeout, ready: ready}
}

// ensure makes the sandbox usable exactly once per readiness epoch. Callers
// serialize on the handle so concurrent tool calls never race to resume.
func (s *sandbox) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, s.readyTimeout)
	defer cancel()
	if err := s.client.ensureReady(readyCtx, s.id, s.ttlSeconds); err != nil {
		return err
	}
	if s.workdir != "." {
		// The command API rejects a cwd that does not exist.
		res, err := s.client.runCommand(readyCtx, s.id, ".", "mkdir -p -- "+shellQuote(s.workdir))
		if err != nil {
			return fmt.Errorf("boatenv: create workdir: %w", err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("boatenv: create workdir exited %d", res.ExitCode)
		}
	}
	s.ready = true
	return nil
}

func (s *sandbox) invalidate() {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
}

// run executes command in the workdir. A 409 means the API refused to start
// it because the sandbox is not running (for example, its TTL archived it
// mid-session); the command never ran, so the handle becomes ready again and
// retries once.
func (s *sandbox) run(ctx context.Context, command string) (commandResult, error) {
	if err := s.ensure(ctx); err != nil {
		return commandResult{}, err
	}
	res, err := s.client.runCommand(ctx, s.id, s.workdir, command)
	if !errors.Is(err, errSandboxNotRunning) {
		return res, err
	}
	s.invalidate()
	if err := s.ensure(ctx); err != nil {
		return commandResult{}, err
	}
	return s.client.runCommand(ctx, s.id, s.workdir, command)
}

// stage uploads data to fresh files under /tmp in the guest, split to the
// files API's per-call limit, and returns their paths in order.
func (s *sandbox) stage(ctx context.Context, data []byte) ([]string, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	nonce, err := randomToken()
	if err != nil {
		return nil, err
	}
	const chunk = 4 << 20
	var paths []string
	for i := 0; i == 0 || i*chunk < len(data); i++ {
		end := min((i+1)*chunk, len(data))
		path := fmt.Sprintf("/tmp/mecatl-boat-stage-%s-%04d", nonce, i)
		if err := s.client.putFile(ctx, s.id, path, data[i*chunk:end]); err != nil {
			return nil, fmt.Errorf("boatenv: stage request: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func randomToken() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("boatenv: random token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	out := []byte{'\''}
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, `'\''`...)
			continue
		}
		out = append(out, s[i])
	}
	return string(append(out, '\''))
}
