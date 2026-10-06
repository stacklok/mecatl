package modelhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sync"
)

const (
	maxContextualGrantSessions    = 1024
	maxContextualGrantsPerSession = 256
	contextualGrantDomain         = "mecatl/guardrail-grant/v1"
)

// WaiverHolder owns process-local keyed contextual repeat grants. Despite its
// compatibility name, it no longer stores normalized Shell/JSON waiver scopes:
// every grant is an opaque HMAC-SHA256 over canonical length-delimited exact
// action and dependency fields assembled by composition.
type WaiverHolder struct {
	mu     sync.Mutex
	key    [32]byte
	grants map[string]map[string]struct{}
}

// NewWaiverHolder constructs an empty holder with a process-local random key.
func NewWaiverHolder() *WaiverHolder {
	h := &WaiverHolder{grants: make(map[string]map[string]struct{})}
	if _, err := rand.Read(h.key[:]); err != nil {
		panic("modelhook: cannot initialize contextual grant key: " + err.Error())
	}
	return h
}

// Digest returns an opaque, domain-separated HMAC-SHA256 over canonical
// length-delimited fields. Exact bytes are preserved; no whitespace or JSON
// normalization is applied.
func (h *WaiverHolder) Digest(parts ...[]byte) string {
	if h == nil {
		return ""
	}
	mac := hmac.New(sha256.New, h.key[:])
	_, _ = mac.Write([]byte(contextualGrantDomain))
	_, _ = mac.Write([]byte{0})
	var length [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write(part)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ArmDigest records one compatibility unscoped digest.
func (h *WaiverHolder) ArmDigest(digest string) { h.ArmDigestForSession("", digest) }

// ArmDigestForSession records one keyed digest under its exact session owner.
func (h *WaiverHolder) ArmDigestForSession(sessionID, digest string) {
	if h == nil || digest == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grants[sessionID] == nil {
		if len(h.grants) >= maxContextualGrantSessions {
			return
		}
		h.grants[sessionID] = make(map[string]struct{})
	}
	if _, exists := h.grants[sessionID][digest]; exists {
		return
	}
	if len(h.grants[sessionID]) >= maxContextualGrantsPerSession {
		return
	}
	h.grants[sessionID][digest] = struct{}{}
}

// CanArm reports whether a new repeat grant can be represented without
// exceeding the process/session memory bounds.
func (h *WaiverHolder) CanArm(sessionID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	grants, exists := h.grants[sessionID]
	if !exists {
		return len(h.grants) < maxContextualGrantSessions
	}
	return len(grants) < maxContextualGrantsPerSession
}

// AllowsDigest reports whether the exact keyed contextual grant is live.
func (h *WaiverHolder) AllowsDigest(digest string) bool {
	if h == nil || digest == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, grants := range h.grants {
		if _, ok := grants[digest]; ok {
			return true
		}
	}
	return false
}

// ClearSession destroys every repeat grant owned by sessionID.
func (h *WaiverHolder) ClearSession(sessionID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.grants, sessionID)
}

// ArmFromApproval is retained for the adapter's legacy Checker-only test seam.
// It uses exact bytes and the same keyed encoding; production contextual action
// review never calls it.
func (h *WaiverHolder) ArmFromApproval(sessionID, tool, key string) {
	h.ArmDigestForSession(sessionID, h.Digest([]byte("legacy-checker-test"), []byte(sessionID), []byte(tool), []byte(key)))
}

// Allows is the exact-byte counterpart to ArmFromApproval for the legacy
// Checker-only test seam. No whitespace normalization is performed.
func (h *WaiverHolder) Allows(sessionID, tool, key string) bool {
	return h.AllowsDigest(h.Digest([]byte("legacy-checker-test"), []byte(sessionID), []byte(tool), []byte(key)))
}
