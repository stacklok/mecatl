package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	maxReviewDetailRunes         = 1000
	maxReviewDetailInputBytes    = 4 * 1024
	maxReviewDetailEntries       = 4096
	maxReviewDetailRegistryBytes = 16 * 1024 * 1024
	maxReviewIDBytes             = 256
)

// GuardrailCoverage is the composition-owned effective checker projection for one session.
type GuardrailCoverage struct {
	Enabled                           bool
	CheckerProviderID, CheckerModelID string
	Entries                           []GuardrailCoverageEntry
}

// GuardrailCoverageEntry describes one effective tool-direction rule.
type GuardrailCoverageEntry struct {
	Tool, Phase, Job, Mode, RuleID, RuleOrigin, Inspection, Reason string
}

// GuardrailReviewDetail is bounded live-only human display data.
type GuardrailReviewDetail struct {
	ReviewID, Concern, SourceDisplay, NextAction string
}

type reviewDetailKey struct {
	session session.SessionID
	review  string
}

type reviewDetailEntry struct {
	root   session.SessionID
	detail GuardrailReviewDetail
}

// ReviewDetailRegistry owns bounded live review detail by delegation root.
type ReviewDetailRegistry struct {
	mu      sync.Mutex
	details map[reviewDetailKey]reviewDetailEntry
	refs    map[session.SessionID]map[session.SessionID]struct{}
	bytes   int64
}

// NewReviewDetailRegistry creates an empty bounded live-detail registry.
func NewReviewDetailRegistry() *ReviewDetailRegistry {
	return &ReviewDetailRegistry{details: make(map[reviewDetailKey]reviewDetailEntry), refs: make(map[session.SessionID]map[session.SessionID]struct{})}
}

func configuredReviewDetailRegistry(configured *ReviewDetailRegistry) *ReviewDetailRegistry {
	if configured != nil {
		return configured
	}
	return NewReviewDetailRegistry()
}

// PublishReviewDetail satisfies agent.ReviewDetailSink. Root-aware engine paths use
// PublishReviewDetailForRoot; this compatibility method deliberately does not mint
// a parent-child relation from an unbound child id.
func (r *ReviewDetailRegistry) PublishReviewDetail(_ context.Context, detail agent.ReviewDetail) {
	r.PublishReviewDetailForRoot(context.Background(), detail.SessionID, detail)
}

// PublishReviewDetailForRoot binds detail to the exact delegation root that produced it.
func (r *ReviewDetailRegistry) PublishReviewDetailForRoot(_ context.Context, root session.SessionID, detail agent.ReviewDetail) {
	if r == nil || root == "" || detail.SessionID == "" || detail.ReviewID == "" || len(detail.ReviewID) > maxReviewIDBytes {
		return
	}
	safe := GuardrailReviewDetail{
		ReviewID: detail.ReviewID,
		Concern:  safeReviewDetail(detail.Concern), SourceDisplay: safeReviewDetail(detail.SourceDisplay), NextAction: safeReviewDetail(detail.NextAction),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := reviewDetailKey{session: detail.SessionID, review: detail.ReviewID}
	old, replacing := r.details[key]
	if replacing && old.root == root {
		safe.Concern = joinReviewDetail(old.detail.Concern, safe.Concern)
		safe.SourceDisplay = joinReviewDetail(old.detail.SourceDisplay, safe.SourceDisplay)
	}
	oldBytes := int64(0)
	if replacing {
		oldBytes = reviewDetailSize(key, old)
	}
	entry := reviewDetailEntry{root: root, detail: safe}
	newBytes := reviewDetailSize(key, entry)
	if (!replacing && len(r.details) >= maxReviewDetailEntries) || newBytes-oldBytes > int64(maxReviewDetailRegistryBytes)-r.bytes {
		return
	}
	if r.refs[root] == nil {
		r.refs[root] = make(map[session.SessionID]struct{})
	}
	r.refs[root][root] = struct{}{}
	r.refs[root][detail.SessionID] = struct{}{}
	r.details[key] = entry
	r.bytes += newBytes - oldBytes
}

func reviewDetailSize(key reviewDetailKey, entry reviewDetailEntry) int64 {
	return int64(len(key.session) + len(key.review) + len(entry.root) + len(entry.detail.ReviewID) + len(entry.detail.Concern) + len(entry.detail.SourceDisplay) + len(entry.detail.NextAction))
}

func joinReviewDetail(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" || left == right {
		return left
	}
	return safeReviewDetail(left + "; " + right)
}

func safeReviewDetail(value string) string {
	if len(value) > maxReviewDetailInputBytes {
		value = value[:maxReviewDetailInputBytes]
	}
	value = session.ToValidUTF8(value)
	value = governance.NeutraliseFraming(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxReviewDetailRunes {
		value = string(runes[:maxReviewDetailRunes-1]) + "…"
	}
	return value
}

func (r *ReviewDetailRegistry) get(ref session.SessionID, review string) (session.SessionID, GuardrailReviewDetail, bool) {
	if r == nil {
		return "", GuardrailReviewDetail{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.details[reviewDetailKey{session: ref, review: review}]
	if !ok {
		return "", GuardrailReviewDetail{}, false
	}
	if _, registered := r.refs[entry.root][ref]; !registered {
		return "", GuardrailReviewDetail{}, false
	}
	return entry.root, entry.detail, true
}

func (r *ReviewDetailRegistry) clear(root session.SessionID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.details {
		if entry.root == root {
			r.bytes -= reviewDetailSize(key, entry)
			delete(r.details, key)
		}
	}
	delete(r.refs, root)
}

func (r *ReviewDetailRegistry) clearAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.details)
	clear(r.refs)
	r.bytes = 0
}

// ListGuardrailCoverage owner-authorizes before asking composition for the exact
// session-specific catalog/rule projection.
func (s *Service) ListGuardrailCoverage(ctx context.Context, id session.SessionID) (GuardrailCoverage, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return GuardrailCoverage{}, err
	}
	if s.cfg.GuardrailCoverage == nil {
		return GuardrailCoverage{}, nil
	}
	return s.cfg.GuardrailCoverage(sess), nil
}

// GetGuardrailReviewDetail reads only live process-local detail. It first owner-
// authorizes the presented root/child session handle, then requires the registry's
// exact root relation, and finally reauthorizes the root. Foreign, stale, and
// arbitrary child handles are concealed identically.
func (s *Service) GetGuardrailReviewDetail(ctx context.Context, ref session.SessionID, reviewID string) (GuardrailReviewDetail, error) {
	if _, err := s.GetSession(ctx, ref); err != nil {
		return GuardrailReviewDetail{}, err
	}
	root, detail, ok := s.reviewDetails.get(ref, reviewID)
	if !ok {
		return GuardrailReviewDetail{}, fmt.Errorf("%w: %q", ErrNotFound, ref)
	}
	if root != ref {
		if _, err := s.GetSession(ctx, root); err != nil {
			return GuardrailReviewDetail{}, err
		}
	}
	return detail, nil
}

func (s *Service) clearGuardrailReviewDetails(root session.SessionID) { s.reviewDetails.clear(root) }
