package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Resource names the independently-accounted admission dimensions.
type Resource string

const (
	// ResourceBootingVMs accounts VMs between admission and readiness.
	ResourceBootingVMs Resource = "booting-vms"
	// ResourceActiveVMs accounts ready or conservatively retained VMs.
	ResourceActiveVMs Resource = "active-vms"
	// ResourceWorktrees accounts prepared session worktrees.
	ResourceWorktrees Resource = "worktrees"
	// ResourceCPU accounts virtual CPUs.
	ResourceCPU Resource = "cpu"
	// ResourceRAMBytes accounts guest memory bytes.
	ResourceRAMBytes Resource = "ram-bytes"
	// ResourceDiskBytes accounts guest and worktree disk bytes.
	ResourceDiskBytes Resource = "disk-bytes"
	// ResourceInodes accounts guest and worktree filesystem entries.
	ResourceInodes Resource = "inodes"
	// ResourceExecs accounts concurrent guest executions.
	ResourceExecs Resource = "execs"
	// ResourceForks accounts concurrent environment forks.
	ResourceForks Resource = "forks"
	// ResourcePulls accounts concurrent immutable artifact pulls.
	ResourcePulls Resource = "pulls"
	// ResourceBootRate accounts VM starts in a rolling time window.
	ResourceBootRate Resource = "boot-rate"
)

// AdmissionScope identifies which independently-enforced quota rejected work.
type AdmissionScope string

const (
	// AdmissionScopeUser identifies an owner-specific quota.
	AdmissionScopeUser AdmissionScope = "user"
	// AdmissionScopeDeployment identifies a daemon-wide quota.
	AdmissionScopeDeployment AdmissionScope = "deployment"
)

// ResourceUsage is a reservation delta. Boots records boot attempts for rate
// limiting and is not retained in concurrent usage after admission.
type ResourceUsage struct {
	BootingVMs int64
	ActiveVMs  int64
	Worktrees  int64
	CPU        int64
	RAMBytes   int64
	DiskBytes  int64
	Inodes     int64
	Execs      int64
	Forks      int64
	Pulls      int64
	Boots      int64
}

// RateLimit bounds admitted boots in one rolling window. A zero Count disables it.
type RateLimit struct {
	Count  int64
	Window time.Duration
}

// AdmissionLimits applies every positive bound to both the owner and deployment.
type AdmissionLimits struct {
	PerUser            ResourceUsage
	Deployment         ResourceUsage
	PerUserBootRate    RateLimit
	DeploymentBootRate RateLimit
}

// AdmissionError is stable and actionable for callers and operator diagnostics.
type AdmissionError struct {
	Scope    AdmissionScope
	Owner    string
	Resource Resource
	Used     int64
	Request  int64
	Limit    int64
}

func (e *AdmissionError) Error() string {
	if e == nil {
		return "microvm admission rejected"
	}
	owner := "deployment"
	if e.Scope == AdmissionScopeUser {
		owner = e.Owner
	}
	return fmt.Sprintf("microvm admission rejected: scope=%s owner=%q resource=%s used=%d request=%d limit=%d", e.Scope, owner, e.Resource, e.Used, e.Request, e.Limit)
}

// AdmissionController atomically accounts per-owner and deployment resources.
type AdmissionController struct {
	mu         sync.Mutex
	limits     AdmissionLimits
	now        func() time.Time
	users      map[string]ResourceUsage
	deployment ResourceUsage
	userBoots  map[string][]time.Time
	boots      []time.Time
}

// NewAdmissionController validates limits and constructs an empty controller.
func NewAdmissionController(limits AdmissionLimits, now func() time.Time) (*AdmissionController, error) {
	if now == nil {
		now = time.Now
	}
	if err := validateUsage(limits.PerUser); err != nil {
		return nil, fmt.Errorf("per-user admission limits: %w", err)
	}
	if err := validateUsage(limits.Deployment); err != nil {
		return nil, fmt.Errorf("deployment admission limits: %w", err)
	}
	if err := validateRateLimit(limits.PerUserBootRate); err != nil {
		return nil, fmt.Errorf("per-user boot-rate admission limit: %w", err)
	}
	if err := validateRateLimit(limits.DeploymentBootRate); err != nil {
		return nil, fmt.Errorf("deployment boot-rate admission limit: %w", err)
	}
	return &AdmissionController{limits: limits, now: now, users: make(map[string]ResourceUsage), userBoots: make(map[string][]time.Time)}, nil
}

// Acquire atomically reserves usage or returns an AdmissionError.
func (a *AdmissionController) Acquire(owner string, request ResourceUsage) (*AdmissionLease, error) {
	return a.AcquireContext(context.Background(), owner, request)
}

// AcquireContext is Acquire with cancellation checked before accounting.
func (a *AdmissionController) AcquireContext(ctx context.Context, owner string, request ResourceUsage) (*AdmissionLease, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("microvm admission is not configured")
	}
	if owner == "" {
		return nil, errors.New("microvm admission owner is required")
	}
	if err := validateUsage(request); err != nil {
		return nil, fmt.Errorf("microvm admission request: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkLocked(owner, ResourceUsage{}, request); err != nil {
		return nil, err
	}
	a.applyLocked(owner, ResourceUsage{}, request)
	a.recordBootsLocked(owner, request.Boots)
	return &AdmissionLease{controller: a, owner: owner, usage: withoutBoots(request)}, nil
}

// Reconstruct restores every still-live durable reservation after daemon
// restart. Destroyed records are intentionally excluded: their durable state is
// the release commit boundary.
func (a *AdmissionController) Reconstruct(records []EnvironmentRecord) error {
	if a == nil {
		return errors.New("microvm admission is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users = make(map[string]ResourceUsage)
	a.deployment = ResourceUsage{}
	for _, record := range records {
		if record.State == EnvironmentDestroyed || record.Owner == "" || record.AdmissionUsage == (ResourceUsage{}) {
			continue
		}
		if err := validateUsage(record.AdmissionUsage); err != nil {
			return fmt.Errorf("reconstruct microvm admission for %s: %w", record.EnvironmentID, err)
		}
		if err := a.checkLocked(record.Owner, ResourceUsage{}, record.AdmissionUsage); err != nil {
			return fmt.Errorf("reconstruct microvm admission for %s: %w", record.EnvironmentID, err)
		}
		a.applyLocked(record.Owner, ResourceUsage{}, record.AdmissionUsage)
	}
	return nil
}

// Release returns a durable generation's retained capacity after its destroyed
// tombstone has been committed. It is intentionally separate from lease release
// so restart-reconstructed reservations follow the same boundary.
func (a *AdmissionController) Release(owner string, usage ResourceUsage) {
	if a == nil || owner == "" || usage == (ResourceUsage{}) {
		return
	}
	a.mu.Lock()
	a.applyLocked(owner, usage, ResourceUsage{})
	a.mu.Unlock()
}

// AdmissionLease owns one reservation. Release is idempotent.
type AdmissionLease struct {
	mu         sync.Mutex
	controller *AdmissionController
	owner      string
	usage      ResourceUsage
	released   bool
}

// Replace atomically transitions a reservation, for example booting VM to active VM.
func (l *AdmissionLease) Replace(next ResourceUsage) error {
	if l == nil {
		return errors.New("microvm admission lease is nil")
	}
	if err := validateUsage(next); err != nil {
		return fmt.Errorf("microvm admission replacement: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errors.New("microvm admission lease is released")
	}
	a := l.controller
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkLocked(l.owner, l.usage, next); err != nil {
		return err
	}
	a.applyLocked(l.owner, l.usage, next)
	a.recordBootsLocked(l.owner, next.Boots)
	l.usage = withoutBoots(next)
	return nil
}

// Release returns all retained capacity exactly once.
func (l *AdmissionLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.controller.mu.Lock()
	l.controller.applyLocked(l.owner, l.usage, ResourceUsage{})
	l.controller.mu.Unlock()
	l.released = true
}

func (a *AdmissionController) checkLocked(owner string, previous, next ResourceUsage) error {
	user := subtractUsage(a.users[owner], previous)
	deployment := subtractUsage(a.deployment, previous)
	if err := checkUsage(AdmissionScopeUser, owner, user, next, a.limits.PerUser); err != nil {
		return err
	}
	if err := checkUsage(AdmissionScopeDeployment, owner, deployment, next, a.limits.Deployment); err != nil {
		return err
	}
	if next.Boots > 0 {
		now := a.now()
		if limit := a.limits.PerUserBootRate; limit.Count > 0 {
			a.userBoots[owner] = trimBoots(a.userBoots[owner], now.Add(-limit.Window))
			if exceeds(int64(len(a.userBoots[owner])), next.Boots, limit.Count) {
				return &AdmissionError{Scope: AdmissionScopeUser, Owner: owner, Resource: ResourceBootRate, Used: int64(len(a.userBoots[owner])), Request: next.Boots, Limit: limit.Count}
			}
		}
		if limit := a.limits.DeploymentBootRate; limit.Count > 0 {
			a.boots = trimBoots(a.boots, now.Add(-limit.Window))
			if exceeds(int64(len(a.boots)), next.Boots, limit.Count) {
				return &AdmissionError{Scope: AdmissionScopeDeployment, Owner: owner, Resource: ResourceBootRate, Used: int64(len(a.boots)), Request: next.Boots, Limit: limit.Count}
			}
		}
	}
	return nil
}

func (a *AdmissionController) applyLocked(owner string, previous, next ResourceUsage) {
	a.users[owner] = addUsage(subtractUsage(a.users[owner], previous), withoutBoots(next))
	a.deployment = addUsage(subtractUsage(a.deployment, previous), withoutBoots(next))
}

func (a *AdmissionController) recordBootsLocked(owner string, count int64) {
	for range count {
		now := a.now()
		if a.limits.PerUserBootRate.Count > 0 {
			a.userBoots[owner] = append(a.userBoots[owner], now)
		}
		if a.limits.DeploymentBootRate.Count > 0 {
			a.boots = append(a.boots, now)
		}
	}
}

func checkUsage(scope AdmissionScope, owner string, used, request, limit ResourceUsage) error {
	for _, item := range usageItems(used, request, limit) {
		if item.limit > 0 && exceeds(item.used, item.request, item.limit) {
			return &AdmissionError{Scope: scope, Owner: owner, Resource: item.resource, Used: item.used, Request: item.request, Limit: item.limit}
		}
	}
	return nil
}

type usageItem struct {
	resource             Resource
	used, request, limit int64
}

func usageItems(used, request, limit ResourceUsage) []usageItem {
	return []usageItem{
		{ResourceBootingVMs, used.BootingVMs, request.BootingVMs, limit.BootingVMs},
		{ResourceActiveVMs, used.ActiveVMs, request.ActiveVMs, limit.ActiveVMs},
		{ResourceWorktrees, used.Worktrees, request.Worktrees, limit.Worktrees},
		{ResourceCPU, used.CPU, request.CPU, limit.CPU},
		{ResourceRAMBytes, used.RAMBytes, request.RAMBytes, limit.RAMBytes},
		{ResourceDiskBytes, used.DiskBytes, request.DiskBytes, limit.DiskBytes},
		{ResourceInodes, used.Inodes, request.Inodes, limit.Inodes},
		{ResourceExecs, used.Execs, request.Execs, limit.Execs},
		{ResourceForks, used.Forks, request.Forks, limit.Forks},
		{ResourcePulls, used.Pulls, request.Pulls, limit.Pulls},
	}
}

func validateUsage(usage ResourceUsage) error {
	for _, item := range usageItems(ResourceUsage{}, usage, ResourceUsage{}) {
		if item.request < 0 {
			return fmt.Errorf("%s must not be negative", item.resource)
		}
	}
	if usage.Boots < 0 {
		return errors.New("boots must not be negative")
	}
	return nil
}

func validateRateLimit(limit RateLimit) error {
	if limit.Count < 0 || limit.Window < 0 || (limit.Count == 0) != (limit.Window == 0) {
		return errors.New("count and window must be positive or both disabled")
	}
	return nil
}

func exceeds(used, request, limit int64) bool {
	return used > limit || request > limit-used
}

func addUsage(a, b ResourceUsage) ResourceUsage {
	return ResourceUsage{BootingVMs: a.BootingVMs + b.BootingVMs, ActiveVMs: a.ActiveVMs + b.ActiveVMs, Worktrees: a.Worktrees + b.Worktrees, CPU: a.CPU + b.CPU, RAMBytes: a.RAMBytes + b.RAMBytes, DiskBytes: a.DiskBytes + b.DiskBytes, Inodes: a.Inodes + b.Inodes, Execs: a.Execs + b.Execs, Forks: a.Forks + b.Forks, Pulls: a.Pulls + b.Pulls}
}
func subtractUsage(a, b ResourceUsage) ResourceUsage {
	return ResourceUsage{BootingVMs: a.BootingVMs - b.BootingVMs, ActiveVMs: a.ActiveVMs - b.ActiveVMs, Worktrees: a.Worktrees - b.Worktrees, CPU: a.CPU - b.CPU, RAMBytes: a.RAMBytes - b.RAMBytes, DiskBytes: a.DiskBytes - b.DiskBytes, Inodes: a.Inodes - b.Inodes, Execs: a.Execs - b.Execs, Forks: a.Forks - b.Forks, Pulls: a.Pulls - b.Pulls}
}
func withoutBoots(usage ResourceUsage) ResourceUsage { usage.Boots = 0; return usage }
func trimBoots(in []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(in) && !in[i].After(cutoff) {
		i++
	}
	return in[i:]
}
