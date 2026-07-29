package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// escapepolicy.go is the path-escape-posture Scenario 2 decision half
// (docs/acceptance/path-escape-posture.md): a root-aware wrapping
// port.PermissionPolicy that relaxes an out-of-root READ escape at the
// yolo/auto operator postures. It is COMPOSITION, not domain — the escape
// decision is a posture/policy concern, and the osfs containment vetting is
// never stripped (the relaxed workspace still canonicalize-then-rejects and
// serves through a fresh *os.Root).
//
// Deny-dominance is preserved by construction: the wrapper DELEGATES TO THE
// INNER POLICY FIRST and only ever relaxes a non-deny — it NEVER converts an
// inner Deny (a configured deny, the plan-mode hard-deny, or a configured
// Ask) into an escape Allow. At strict/trusted it returns the inner decision
// verbatim (this wave changes nothing below auto — the strict/trusted Ask is
// Scenario 4).

// escapePolicy wraps an inner port.PermissionPolicy with the posture-derived
// out-of-root read-escape decision, keyed to the session workspace root on
// EVERY Evaluate (via the ws the loop hands it). It is a permpolicy sibling:
// it implements the SAME port.PermissionPolicy seam, delegating the ordinary
// deny/ask/allow fold to the inner policy and layering ONLY the escape
// decision on top. The shared engine carries ONE instance; per-session
// root-awareness comes from ws.Root(), so two sessions on different roots
// classify correctly through the same wrapper.
//
// The classifier is resolved per root (cached): a session root's classifier
// is built once and reused, so the per-Evaluate cost is a map read after the
// first call for that root. Learn is forwarded so an "allow always" verdict
// on a non-escape call still records against the inner store.
type escapePolicy struct {
	inner     port.PermissionPolicy
	posture   Posture
	readRoots []string

	mu   sync.Mutex
	clfs map[string]*escapeClassifier // session root → classifier (built once per root)
}

// newEscapePolicy builds the shared-engine wrapper. readRoots are the session's
// WithReadRoots read-only roots (the skills carve-out), so each per-root
// classifier's read-root verdict matches the workspace's. It is a NO-OP
// pass-through below PostureAuto (strict/trusted change nothing this wave) —
// the caller may still install it uniformly and rely on the posture gate.
func newEscapePolicy(inner port.PermissionPolicy, posture Posture, readRoots []string) port.PermissionPolicy {
	return &escapePolicy{
		inner:     inner,
		posture:   posture,
		readRoots: readRoots,
		clfs:      make(map[string]*escapeClassifier),
	}
}

// classifierFor returns the escape classifier for the session's workspace
// root, building and caching it on first use. A nil ws or an unclassifiable
// root yields nil (the Evaluate then leaves the call to the inner policy —
// fail-safe, never an invented relax).
func (p *escapePolicy) classifierFor(ws tool.WorkspaceReader) *escapeClassifier {
	if ws == nil {
		return nil
	}
	root := ws.Root()
	if root == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clfs[root]; ok {
		return c
	}
	c, err := newEscapeClassifier(root, p.readRoots...)
	if err != nil {
		return nil
	}
	p.clfs[root] = c
	return c
}

// Evaluate delegates to the inner policy FIRST, then applies the escape
// decision only when the inner did not deny. The fold:
//
//  1. inner Deny → returned verbatim (deny-dominance: a configured deny, the
//     plan-mode hard-deny, or any inner deny is never relaxed);
//  2. pseudo-fs (/proc, /sys, /dev) → Deny at EVERY posture (the never-relaxed
//     category — an in-process Read of /proc/self/environ would return the
//     SERVER's raw, unscrubbed environment, a channel the envscrub-scrubbed
//     Bash parity path does not provide);
//  3. an out-of-root READ escape at auto/yolo → Allow (Bash parity);
//  4. everything else → the inner decision verbatim (strict/trusted unchanged
//     this wave; a write escape is not relaxed by Scenario 2).
func (p *escapePolicy) Evaluate(ctx context.Context, sessionID session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) governance.PermissionDecision {
	decision := p.inner.Evaluate(ctx, sessionID, mode, c, ws)
	if decision.Effect == governance.Deny {
		return decision // deny-dominance: never relax an inner deny
	}
	clf := p.classifierFor(ws)
	if clf == nil {
		return decision
	}
	kind := clf.classify(c.Name, c.Args)
	switch kind {
	case escapePseudoFS:
		// Never-relaxed at every posture: an FS read of a pseudo-fs path would
		// exfiltrate the SERVER's raw environment, a channel Bash does not
		// provide (the env-scrub gotcha). Hard-deny even at yolo.
		return governance.PermissionDecision{
			Effect: governance.Deny,
			Reason: "path lies under a pseudo-filesystem (/proc, /sys, /dev): an in-process FS read would expose the server's raw environment — never relaxed at any posture",
		}
	case escapeEscape:
		if p.posture >= PostureAuto && c.Name == "Read" {
			// Bash parity: at auto/yolo Bash already reads the same bytes, so
			// the FS read boundary was cosmetic. The relaxed workspace serves
			// the read; the wrapper only has to not stand in its way.
			return governance.PermissionDecision{Effect: governance.Allow}
		}
	}
	return decision
}

// Learn forwards rule-learning to the inner policy (an "allow always" verdict
// on an ordinary call still records against the inner learned-rule store).
// The escape relax is posture-derived, never learned, so Learn has no
// escape-specific behaviour.
func (p *escapePolicy) Learn(sessionID session.SessionID, c session.ToolCall) {
	p.inner.Learn(sessionID, c)
}

// --- the workspace wrapper (the relaxed-read serving half) ---

// escapeWorkspace is the MAIN session's relaxed-read workspace: it wraps the
// posture-relaxed *osfs.Workspace (built WithRelaxedReads at auto/yolo) and
// carries the session's escapeClassifier so Read/Stat consult the SAME
// pseudo-fs decision before delegating — the workspace and the permission
// wrapper classify over the SAME root, never two classifiers that could drift
// (the single-construction guarantee: only osfsWorkspaceFactory builds the
// pair, only for the main session).
//
// The wrapper's job is the pseudo-fs hard-deny at the TOOL-BODY boundary: the
// policy Evaluate already refuses pseudo-fs before dispatch, so Read hitting
// the wrapper's refusal is defense-in-depth. Every other method (Write/Glob/
// Grep/the Edit ledger) delegates unchanged: the relax is READ-ONLY, and a
// write escape is Scenario 3 (never relaxed by this wrapper).
type escapeWorkspace struct {
	tool.Workspace                   // the relaxed *osfs.Workspace (WithRelaxedReads)
	classifier     *escapeClassifier // the SAME classification the policy wrapper uses
}

// newEscapeWorkspace wraps a relaxed ws with the escape classifier. It is
// built ONLY by osfsWorkspaceFactory, ONLY at auto/yolo (relaxedReads on),
// ONLY for the main session's workspace factory — never a fork/child
// workspace.
func newEscapeWorkspace(ws tool.Workspace, classifier *escapeClassifier) tool.Workspace {
	return &escapeWorkspace{Workspace: ws, classifier: classifier}
}

// Read consults the escape classifier (pseudo-fs hard-deny) then delegates to
// the relaxed osfs Read. An ordinary in-root/read-root path is untouched.
func (w *escapeWorkspace) Read(ctx context.Context, path string) ([]byte, error) {
	if err := w.refusePseudoFS(path); err != nil {
		return nil, err
	}
	return w.Workspace.Read(ctx, path)
}

// Stat consults the escape classifier (pseudo-fs hard-deny) then delegates.
func (w *escapeWorkspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	if err := w.refusePseudoFS(path); err != nil {
		return tool.FileInfo{}, err
	}
	return w.Workspace.Stat(ctx, path)
}

// refusePseudoFS returns the hard-deny error when path classifies pseudo-fs
// under the session's classifier — the SAME never-relaxed refusal the
// permission wrapper applies, defense-in-depth at the tool-body boundary.
func (w *escapeWorkspace) refusePseudoFS(path string) error {
	args, _ := json.Marshal(map[string]string{"path": path})
	if w.classifier.classify("Read", args) == escapePseudoFS {
		return errors.New("osfs: path escapes workspace root: pseudo-filesystem (/proc, /sys, /dev) is never served at any posture")
	}
	return nil
}
