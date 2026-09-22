// Package jevrouter adapts Typesafe System One decisions to Mecatl's delegated-model router.
package jevrouter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	typesafe "github.com/stacklok/typesafe-go"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	defaultModel                 = "jev-1.13.0"
	questionID                   = "delegated-model-category"
	classifierInstructions       = "Choose exactly one category for the delegated task."
	maxTextBytes                 = 64 * 1024
	maxCategories                = 255
	defaultMaxConcurrent         = 8
	defaultQueueTimeout          = 10 * time.Second
	defaultRequestTimeout        = 10 * time.Second
	responseLimit          int64 = 1 << 20
)

// MissKind is the adapter-local mechanism result for a failed classification.
// Composition translates every nonzero value to the engine's common router outcome taxonomy.
type MissKind uint8

const (
	missNone MissKind = iota
	// MissClassifierError identifies an unclassified transport or SDK failure.
	MissClassifierError
	// MissBadVerdict identifies an invalid response protocol.
	MissBadVerdict
	// MissUnknownCategory identifies a structured offered-set violation.
	MissUnknownCategory
	// MissLowConfidence identifies a valid choice below the configured threshold.
	MissLowConfidence
	// MissInputOverLimit identifies a locally rejected input bound.
	MissInputOverLimit
	// MissCapacityTimeout identifies an expired queue wait while the caller remained active.
	MissCapacityTimeout
	// MissCancelled identifies observed caller cancellation.
	MissCancelled
	// MissTimeout identifies an observed caller, request, or SDK deadline.
	MissTimeout
)

// Category is one trusted operator-authored classification choice.
type Category struct {
	Name        string
	Description string
}

// Options configures one Build-owned Jev router.
type Options struct {
	APIKey            string
	Model             string
	BaseURL           string
	MinimumConfidence float64
	HTTPClient        *http.Client
}

type transportBounds struct {
	maxConcurrent  int
	queueTimeout   time.Duration
	requestTimeout time.Duration
}

func defaultBounds() transportBounds {
	return transportBounds{
		maxConcurrent:  defaultMaxConcurrent,
		queueTimeout:   defaultQueueTimeout,
		requestTimeout: defaultRequestTimeout,
	}
}

// Router owns one concurrent-safe SDK client and one bounded request semaphore.
type Router struct {
	client            *typesafe.Client
	semaphore         chan struct{}
	minimumConfidence float64
	model             string
	queueTimeout      time.Duration
	requestTimeout    time.Duration
}

// New constructs a router without performing network I/O.
func New(opts Options) (*Router, error) {
	return newRouter(opts, defaultBounds())
}

func newRouter(opts Options, bounds transportBounds) (*Router, error) {
	if math.IsNaN(opts.MinimumConfidence) || math.IsInf(opts.MinimumConfidence, 0) || opts.MinimumConfidence < 0 || opts.MinimumConfidence > 1 {
		return nil, fmt.Errorf("jev minimum confidence must be finite and between 0 and 1")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = defaultModel
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = typesafe.DefaultBaseURL
	}
	retries := typesafe.DefaultRetryPolicy()
	retries.MaxRetries = 0
	clientOpts := []typesafe.Option{
		typesafe.WithAPIKey(opts.APIKey),
		typesafe.WithDefaultModel(model),
		typesafe.WithBaseURL(baseURL),
		typesafe.WithAttemptTimeout(bounds.requestTimeout),
		typesafe.WithRetryPolicy(retries),
		typesafe.WithResponseLimit(responseLimit),
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, typesafe.WithHTTPClient(opts.HTTPClient))
	}
	sdk, err := typesafe.NewClient(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("construct Jev client: %w", err)
	}
	return &Router{
		client: sdk, semaphore: make(chan struct{}, bounds.maxConcurrent),
		minimumConfidence: opts.MinimumConfidence, model: model,
		queueTimeout: bounds.queueTimeout, requestTimeout: bounds.requestTimeout,
	}, nil
}

// Route makes exactly one bounded classification attempt and returns an adapter-local miss kind.
func (r *Router) Route(ctx context.Context, task string, categories []Category) (string, session.Usage, MissKind, bool) {
	if len(categories) > maxCategories || requestBytes(task, r.model, categories) > maxTextBytes {
		return "", session.Usage{}, MissInputOverLimit, false
	}
	criteria := make(map[string]typesafe.Content, len(categories))
	for _, category := range categories {
		criteria[category.Name] = category.Description
	}
	queueCtx, cancelQueue := context.WithTimeout(ctx, r.queueTimeout)
	defer cancelQueue()
	select {
	case r.semaphore <- struct{}{}:
		defer func() { <-r.semaphore }()
	case <-queueCtx.Done():
		if kind := contextMiss(ctx.Err()); kind != missNone {
			return "", session.Usage{}, kind, false
		}
		return "", session.Usage{}, MissCapacityTimeout, false
	}
	requestCtx, cancelRequest := context.WithTimeout(ctx, r.requestTimeout)
	defer cancelRequest()
	response, err := r.client.SystemOne(requestCtx, typesafe.SystemOneRequest{
		State: task,
		Model: r.model,
		Questions: map[string]typesafe.Question{
			questionID: typesafe.Choice(classifierInstructions, criteria),
		},
	})
	if err != nil {
		failureUsage, kind := classifyFailure(ctx, requestCtx, err)
		return "", failureUsage, kind, false
	}
	usage := usageFrom(&response.Usage)
	answer, ok := response.Answers[questionID].(typesafe.ChoiceAnswer)
	if !ok {
		return "", usage, MissBadVerdict, false
	}
	if _, offered := criteria[answer.Choice]; !offered {
		return "", usage, MissUnknownCategory, false
	}
	if r.minimumConfidence > 0 && answer.Confidence < r.minimumConfidence {
		return "", usage, MissLowConfidence, false
	}
	return answer.Choice, usage, missNone, true
}

func classifyFailure(callerCtx, operationCtx context.Context, err error) (session.Usage, MissKind) {
	var protocolErr *typesafe.ProtocolError
	isProtocol := errors.As(err, &protocolErr)
	usage := session.Usage{}
	if isProtocol {
		usage = usageFrom(protocolErr.Usage)
	}
	if kind := contextMiss(callerCtx.Err()); kind != missNone {
		return usage, kind
	}
	if kind := contextMiss(operationCtx.Err()); kind != missNone {
		return usage, kind
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, typesafe.ErrAttemptTimeout) {
		return usage, MissTimeout
	}
	if errors.Is(err, context.Canceled) {
		return usage, MissCancelled
	}
	if isProtocol {
		if protocolErr.Field == "answers" && protocolErr.Reason == "out-of-set choice" {
			return usage, MissUnknownCategory
		}
		return usage, MissBadVerdict
	}
	return usage, MissClassifierError
}

func contextMiss(err error) MissKind {
	switch {
	case errors.Is(err, context.Canceled):
		return MissCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return MissTimeout
	default:
		return missNone
	}
}

func requestBytes(task, model string, categories []Category) int {
	total := len(task) + len(classifierInstructions) + len(model) + len(questionID)
	for _, category := range categories {
		total += len(category.Name) + len(category.Description)
	}
	return total
}

func usageFrom(usage *typesafe.Usage) session.Usage {
	if usage == nil {
		return session.Usage{}
	}
	return session.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
}
