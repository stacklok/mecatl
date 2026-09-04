package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
)

const (
	defaultReflectionQueueCapacity          = 64
	defaultReflectionPrincipalQueueCapacity = 8
	defaultReflectionWorkers                = 1
	defaultReflectionJobTimeout             = 2 * time.Minute
	defaultReflectionReceipts               = 128
	defaultReflectionJobBytes               = 256 << 10
	defaultReflectionQueueBytes             = 4 << 20
)

var (
	errReflectionCoordinatorClosed = errors.New("reflection coordinator is closed")
	errReflectionQueueFull         = errors.New("reflection queue is full")
)

type reflectionDisposition string

const (
	reflectionQueued      reflectionDisposition = "queued"
	reflectionDuplicate   reflectionDisposition = "duplicate"
	reflectionQueueFull   reflectionDisposition = "queue_full"
	reflectionClosed      reflectionDisposition = "closed"
	reflectionCompleted   reflectionDisposition = "completed"
	reflectionFailed      reflectionDisposition = "failed"
	reflectionTimedOut    reflectionDisposition = "timed_out"
	reflectionRateLimited reflectionDisposition = "rate_limited"
)

type reflectionReceipt struct {
	ID          string
	Disposition reflectionDisposition
	Queued      int
	Staged      int
	Promoted    int
	Conflicted  int
	Abstained   bool
	ProposalID  learning.ProposalID
	SkillID     learning.SkillID
	Err         string
}

type reflectionJob struct {
	principal     string
	input         learning.Input
	selectedBytes int
	reflector     learning.Reflector
	process       func(context.Context, string, learning.Outcome) (reflectionReceipt, error)
	reserve       func() bool
	complete      func(reflectionReceipt)
}

type queuedReflection struct {
	id, key, digest string
	bytes           int
	job             reflectionJob
}

type reflectionCoordinatorConfig struct {
	Capacity, PrincipalCapacity int
	Workers                     int
	Timeout                     time.Duration
	Receipts                    int
	JobBytes, QueueBytes        int
	Diagnostics                 port.Diagnostics
}

type reflectionReceiptState struct {
	done     chan struct{}
	receipt  reflectionReceipt
	terminal bool
}

// reflectionCoordinator is the one Build-owned scheduler for automatic
// reflection. It bounds process and principal pressure while preserving FIFO
// within a principal and rotating principals after every completed dequeue.
type reflectionCoordinator struct {
	ctx    context.Context
	cancel context.CancelFunc
	cfg    reflectionCoordinatorConfig

	mu           sync.Mutex
	queues       map[string][]queuedReflection
	active       []string
	running      map[string]int
	pending      map[string]string
	queued       int
	queuedBytes  int
	closed       bool
	notify       chan struct{}
	receipts     map[string]*reflectionReceiptState
	receiptOrder []string
	attempt      uint64
	workers      sync.WaitGroup
	started      bool
	closeOnce    sync.Once
}

func newReflectionCoordinator(parent context.Context, cfg reflectionCoordinatorConfig) *reflectionCoordinator {
	if parent == nil {
		parent = context.Background()
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = defaultReflectionQueueCapacity
	}
	if cfg.PrincipalCapacity <= 0 {
		cfg.PrincipalCapacity = defaultReflectionPrincipalQueueCapacity
	}
	if cfg.PrincipalCapacity > cfg.Capacity {
		cfg.PrincipalCapacity = cfg.Capacity
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultReflectionWorkers
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultReflectionJobTimeout
	}
	if cfg.Receipts <= 0 {
		cfg.Receipts = defaultReflectionReceipts
	}
	if cfg.Receipts < cfg.Capacity+cfg.Workers {
		cfg.Receipts = cfg.Capacity + cfg.Workers
	}
	if cfg.JobBytes <= 0 {
		cfg.JobBytes = defaultReflectionJobBytes
	}
	if cfg.QueueBytes <= 0 {
		cfg.QueueBytes = defaultReflectionQueueBytes
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = port.NopDiagnostics{}
	}
	ctx, cancel := context.WithCancel(parent)
	c := &reflectionCoordinator{
		ctx: ctx, cancel: cancel, cfg: cfg,
		queues: make(map[string][]queuedReflection), running: make(map[string]int),
		pending: make(map[string]string), notify: make(chan struct{}, 1),
		receipts: make(map[string]*reflectionReceiptState),
	}
	return c
}

func (c *reflectionCoordinator) startWorkersLocked() {
	if c.started || c.closed {
		return
	}
	c.started = true
	c.workers.Add(c.cfg.Workers)
	for range c.cfg.Workers {
		go c.worker()
	}
}

func reflectionInputMaterial(in learning.Input, limit int) ([]byte, error) {
	if reflectionRawInputBytes(in, limit) > limit {
		return nil, fmt.Errorf("reflection input exceeds %d-byte job limit", limit)
	}
	projection, err := learning.ProjectInput(in)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return nil, fmt.Errorf("marshal reflection input: %w", err)
	}
	return raw, nil
}

//nolint:gocyclo // every retained reflection field contributes to one fail-fast aggregate bound
func reflectionRawInputBytes(in learning.Input, limit int) int {
	total := 1024
	add := func(values ...string) bool {
		for _, value := range values {
			total += len(value) + 32
			if total > limit {
				return false
			}
		}
		return true
	}
	for _, message := range in.Trajectory.Messages {
		if !add(string(message.Role), message.Text) {
			return total
		}
		for _, call := range message.ToolCalls {
			if !add(string(call.ID), call.Name, string(call.Args)) {
				return total
			}
		}
		for _, part := range message.Parts {
			total += len(part.Data)
			if !add(string(part.Kind), string(part.BlockKind), part.MIMEType, part.Text, part.Name, part.Title, part.Description) || total > limit {
				return total
			}
		}
		if message.ToolResult != nil {
			if !add(string(message.ToolResult.CallID), message.ToolResult.Content) {
				return total
			}
			for _, part := range message.ToolResult.Parts {
				total += len(part.Data)
				if !add(string(part.Kind), string(part.BlockKind), part.MIMEType, part.Text, part.Name, part.Title, part.Description) || total > limit {
					return total
				}
			}
		}
	}
	for _, event := range in.Events {
		if !add(string(event.Type), event.Text, string(event.Stop)) {
			return total
		}
		if event.ToolCall != nil && !add(string(event.ToolCall.ID), event.ToolCall.Name) {
			return total
		}
		if event.ToolResult != nil && !add(string(event.ToolResult.CallID), event.ToolResult.Content) {
			return total
		}
	}
	for _, fact := range in.Existing {
		if !add(string(fact.Kind), fact.Key, fact.Value, fact.Description) {
			return total
		}
	}
	return total
}

func reflectionInputDigest(in learning.Input) (string, error) {
	raw, err := reflectionInputMaterial(in, defaultReflectionJobBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func reflectionJobID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "reflection-" + hex.EncodeToString(sum[:8])
}

func (c *reflectionCoordinator) nextAttemptIDLocked(key string) string {
	c.attempt++
	return fmt.Sprintf("%s-%016x", reflectionJobID(key), c.attempt)
}

func selectedEvidenceIdentity(in learning.Input) (string, string, error) {
	manifest := in.Manifest
	if manifest == nil || manifest.Protocol != learning.ReflectionEvidenceV1 ||
		manifest.Source.Domain != learning.ReflectionEvidenceSourceV1 ||
		manifest.Source.Value != in.Trajectory.SessionID || !learning.ValidSHA256(manifest.Digest) {
		return "", "", errors.New("reflection job has invalid selected-evidence identity")
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s", manifest.Protocol, manifest.Source.Domain, manifest.Source.Value, manifest.Digest)
	return key, manifest.Digest, nil
}

// Enqueue admits a job without waiting for model or storage work. An in-flight
// duplicate joins the original attempt; a rerun after completion gets a fresh id.
// Capacity rejection records the same content-free id/count receipt that
// diagnostics report.
func (c *reflectionCoordinator) Enqueue(job reflectionJob) (reflectionReceipt, error) {
	if c == nil {
		return reflectionReceipt{Disposition: reflectionClosed}, errReflectionCoordinatorClosed
	}
	if job.principal == "" || job.reflector == nil {
		return reflectionReceipt{}, errors.New("invalid reflection job")
	}
	if job.selectedBytes <= 0 || job.selectedBytes > c.cfg.JobBytes {
		return reflectionReceipt{}, fmt.Errorf("reflection input exceeds %d-byte job limit", c.cfg.JobBytes)
	}
	identity, digest, err := selectedEvidenceIdentity(job.input)
	if err != nil {
		return reflectionReceipt{}, err
	}
	key := job.principal + "\x00" + identity
	baseID := reflectionJobID(key)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return reflectionReceipt{ID: baseID, Disposition: reflectionClosed}, nil
	}
	if existing, ok := c.pending[key]; ok {
		queued := c.queued
		c.mu.Unlock()
		c.cfg.Diagnostics.Log(c.ctx, port.LevelInfo, "reflection job duplicate", "job_id", existing, "queued", queued)
		return reflectionReceipt{ID: existing, Disposition: reflectionDuplicate, Queued: queued}, nil
	}
	principalQueued := len(c.queues[job.principal]) + c.running[job.principal]
	if c.queued >= c.cfg.Capacity || principalQueued >= c.cfg.PrincipalCapacity || c.queuedBytes+job.selectedBytes > c.cfg.QueueBytes {
		queued := c.queued
		receipt := reflectionReceipt{ID: baseID, Disposition: reflectionQueueFull, Queued: queued, Err: errReflectionQueueFull.Error()}
		c.mu.Unlock()
		c.cfg.Diagnostics.Log(c.ctx, port.LevelInfo, "reflection queue full", "job_id", baseID, "queued", queued, "principal_queued", principalQueued)
		return receipt, nil
	}
	id := c.nextAttemptIDLocked(key)
	if !c.reserveReceiptLocked(id) {
		queued := c.queued
		receipt := reflectionReceipt{ID: id, Disposition: reflectionQueueFull, Queued: queued, Err: errReflectionQueueFull.Error()}
		c.mu.Unlock()
		c.cfg.Diagnostics.Log(c.ctx, port.LevelInfo, "reflection receipt capacity full", "job_id", id, "queued", queued, "principal_queued", principalQueued)
		return receipt, nil
	}
	if job.reserve != nil && !job.reserve() {
		delete(c.receipts, id)
		for i, receiptID := range c.receiptOrder {
			if receiptID == id {
				c.receiptOrder = append(c.receiptOrder[:i], c.receiptOrder[i+1:]...)
				break
			}
		}
		queued := c.queued
		c.mu.Unlock()
		return reflectionReceipt{ID: baseID, Disposition: reflectionRateLimited, Queued: queued}, nil
	}
	if len(c.queues[job.principal]) == 0 && c.running[job.principal] == 0 {
		c.active = append(c.active, job.principal)
	}
	c.queues[job.principal] = append(c.queues[job.principal], queuedReflection{id: id, key: key, digest: digest, bytes: job.selectedBytes, job: job})
	c.pending[key] = id
	c.queued++
	c.queuedBytes += job.selectedBytes
	c.startWorkersLocked()
	queued := c.queued
	c.mu.Unlock()
	c.signal()
	return reflectionReceipt{ID: id, Disposition: reflectionQueued, Queued: queued}, nil
}

func (c *reflectionCoordinator) reserveReceiptLocked(id string) bool {
	if state := c.receipts[id]; state != nil {
		if !state.terminal {
			return true
		}
		delete(c.receipts, id)
		for i, existing := range c.receiptOrder {
			if existing == id {
				c.receiptOrder = append(c.receiptOrder[:i], c.receiptOrder[i+1:]...)
				break
			}
		}
	}
	for len(c.receiptOrder) >= c.cfg.Receipts {
		evict := -1
		for i, old := range c.receiptOrder {
			if state := c.receipts[old]; state != nil && state.terminal {
				evict = i
				break
			}
		}
		if evict < 0 {
			return false
		}
		old := c.receiptOrder[evict]
		delete(c.receipts, old)
		c.receiptOrder = append(c.receiptOrder[:evict], c.receiptOrder[evict+1:]...)
	}
	c.receipts[id] = &reflectionReceiptState{done: make(chan struct{})}
	c.receiptOrder = append(c.receiptOrder, id)
	return true
}

func (c *reflectionCoordinator) publishLocked(receipt reflectionReceipt) {
	state := c.receipts[receipt.ID]
	if state == nil || state.terminal {
		return
	}
	state.receipt = receipt
	state.terminal = true
	close(state.done)
}

// Wait returns the replayable terminal receipt for id. Every concurrent waiter
// observes the same immutable receipt after the close-only notification.
func (c *reflectionCoordinator) Wait(ctx context.Context, id string) (reflectionReceipt, error) {
	c.mu.Lock()
	state := c.receipts[id]
	c.mu.Unlock()
	if state == nil {
		return reflectionReceipt{ID: id, Disposition: reflectionFailed, Err: "reflection receipt not found"}, nil
	}
	select {
	case <-state.done:
		return state.receipt, nil
	case <-ctx.Done():
		return reflectionReceipt{ID: id, Disposition: reflectionQueued}, ctx.Err()
	}
}

func (c *reflectionCoordinator) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func containsPrincipal(values []string, principal string) bool {
	for _, value := range values {
		if value == principal {
			return true
		}
	}
	return false
}

func (c *reflectionCoordinator) dequeue() (queuedReflection, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := -1
	for i, principal := range c.active {
		if c.running[principal] == 0 {
			index = i
			break
		}
	}
	if index < 0 {
		return queuedReflection{}, false
	}
	principal := c.active[index]
	c.active = append(c.active[:index], c.active[index+1:]...)
	queue := c.queues[principal]
	item := queue[0]
	queue = queue[1:]
	c.queued--
	c.queuedBytes -= item.bytes
	c.running[principal]++
	if len(queue) == 0 {
		delete(c.queues, principal)
	} else {
		c.queues[principal] = queue
	}
	return item, true
}

func (c *reflectionCoordinator) finish(item queuedReflection, receipt reflectionReceipt) {
	if item.job.complete != nil {
		item.job.complete(receipt)
	}
	c.mu.Lock()
	delete(c.pending, item.key)
	c.running[item.job.principal]--
	if c.running[item.job.principal] == 0 {
		delete(c.running, item.job.principal)
	}
	if len(c.queues[item.job.principal]) > 0 && !containsPrincipal(c.active, item.job.principal) {
		c.active = append(c.active, item.job.principal)
	}
	c.publishLocked(receipt)
	more := c.queued > 0
	c.mu.Unlock()
	if more {
		c.signal()
	}
}

func (c *reflectionCoordinator) worker() {
	defer c.workers.Done()
	for {
		item, ok := c.dequeue()
		if ok {
			c.run(item)
			continue
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.notify:
		}
	}
}

func (c *reflectionCoordinator) run(item queuedReflection) {
	receipt := reflectionReceipt{ID: item.id, Disposition: reflectionCompleted}
	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.Timeout)
	outcome, err := item.job.reflector.Reflect(ctx, item.job.input)
	if err == nil {
		if outcome.Kind == learning.OutcomeAbstained {
			receipt.Abstained = true
		} else if item.job.process == nil {
			err = errors.New("reflection job has no outcome processor")
		} else {
			var processed reflectionReceipt
			processed, err = item.job.process(ctx, item.digest, outcome)
			processed.ID = item.id
			processed.Disposition = reflectionCompleted
			receipt = processed
		}
	}
	cancel()
	if err != nil {
		receipt.Disposition = reflectionFailed
		failure := "reflection failed"
		if errors.Is(err, context.DeadlineExceeded) {
			receipt.Disposition = reflectionTimedOut
			failure = "reflection timed out"
		} else if errors.Is(err, context.Canceled) {
			failure = "reflection cancelled"
		}
		receipt.Err = failure
		if !errors.Is(err, context.Canceled) || c.ctx.Err() == nil {
			c.cfg.Diagnostics.Log(c.ctx, port.LevelWarn, "reflection job failed", "job_id", item.id, "failure", failure)
		}
	} else {
		c.cfg.Diagnostics.Log(c.ctx, port.LevelInfo, "reflection job completed", "job_id", item.id,
			"staged", receipt.Staged, "promoted", receipt.Promoted, "conflicted", receipt.Conflicted, "abstained", receipt.Abstained)
	}
	c.finish(item, receipt)
}

// Close stops admission, cancels active jobs and joins every worker.
func (c *reflectionCoordinator) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		for _, queue := range c.queues {
			for _, item := range queue {
				delete(c.pending, item.key)
				c.publishLocked(reflectionReceipt{ID: item.id, Disposition: reflectionClosed, Err: errReflectionCoordinatorClosed.Error()})
			}
		}
		c.queues = make(map[string][]queuedReflection)
		c.active = nil
		c.queued = 0
		c.queuedBytes = 0
		c.mu.Unlock()
		c.cancel()
		c.signal()
		c.workers.Wait()
	})
}
