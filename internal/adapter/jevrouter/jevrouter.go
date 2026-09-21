// Package jevrouter adapts Typesafe System One decisions to Mecatl's delegated-model router.
package jevrouter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
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
	maxConcurrent                = 8
	responseLimit          int64 = 1 << 20
	queueTimeout                 = 10 * time.Second
	requestTimeout               = 10 * time.Second

	// MissError is the static miss reason for transport, SDK, timeout, or cancellation failures.
	MissError = "jev-error"
	// MissInvalidResponse is the static miss reason for malformed or semantically invalid responses.
	MissInvalidResponse = "jev-invalid-response"
	// MissLowConfidence is the static miss reason for a choice below the configured threshold.
	MissLowConfidence = "jev-low-confidence"
	// MissOverLimit is the static miss reason for a locally rejected request size.
	MissOverLimit = "jev-over-limit"
	// MissQueueTimeout is the static miss reason for exhausting the bounded queue wait.
	MissQueueTimeout = "jev-queue-timeout"
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

// Router owns one concurrent-safe SDK client and one bounded request semaphore.
type Router struct {
	client            *typesafe.Client
	semaphore         chan struct{}
	minimumConfidence float64
	model             string
	baseURL           string
	httpClient        *http.Client
}

// New constructs a router without performing network I/O.
func New(opts Options) (*Router, error) {
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
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	baseClient := opts.HTTPClient
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	clientCopy := *baseClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	retries := typesafe.DefaultRetryPolicy()
	retries.MaxRetries = 0
	sdk, err := typesafe.NewClient(
		typesafe.WithAPIKey(opts.APIKey),
		typesafe.WithHTTPClient(&clientCopy),
		typesafe.WithDefaultModel(model),
		typesafe.WithBaseURL(baseURL),
		typesafe.WithAttemptTimeout(requestTimeout),
		typesafe.WithRetryPolicy(retries),
		typesafe.WithResponseLimit(responseLimit),
	)
	if err != nil {
		return nil, fmt.Errorf("construct Jev client: %w", err)
	}
	return &Router{
		client: sdk, semaphore: make(chan struct{}, maxConcurrent),
		minimumConfidence: opts.MinimumConfidence, model: model,
		baseURL: baseURL, httpClient: &clientCopy,
	}, nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("jev base URL is invalid")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("jev base URL must use HTTPS or loopback HTTP")
}

// Route makes exactly one bounded classification attempt and returns only static miss reasons.
func (r *Router) Route(ctx context.Context, task string, categories []Category) (string, session.Usage, string, bool) {
	if len(categories) > maxCategories || requestBytes(task, r.model, categories) > maxTextBytes {
		return "", session.Usage{}, MissOverLimit, false
	}
	criteria := make(map[string]typesafe.Content, len(categories))
	for _, category := range categories {
		criteria[category.Name] = category.Description
	}
	queueCtx, cancelQueue := context.WithTimeout(ctx, queueTimeout)
	defer cancelQueue()
	select {
	case r.semaphore <- struct{}{}:
		defer func() { <-r.semaphore }()
	case <-queueCtx.Done():
		if ctx.Err() != nil {
			return "", session.Usage{}, MissError, false
		}
		return "", session.Usage{}, MissQueueTimeout, false
	}
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestTimeout)
	defer cancelRequest()
	response, err := r.client.SystemOne(requestCtx, typesafe.SystemOneRequest{
		State: task,
		Model: r.model,
		Questions: map[string]typesafe.Question{
			questionID: typesafe.Choice(classifierInstructions, criteria),
		},
	})
	if err != nil {
		var protocolErr *typesafe.ProtocolError
		if errors.As(err, &protocolErr) {
			return "", usageFrom(protocolErr.Usage), MissInvalidResponse, false
		}
		return "", session.Usage{}, MissError, false
	}
	usage := usageFrom(&response.Usage)
	answer, ok := response.Answers[questionID].(typesafe.ChoiceAnswer)
	if !ok {
		return "", usage, MissInvalidResponse, false
	}
	if _, offered := criteria[answer.Choice]; !offered {
		return "", usage, MissInvalidResponse, false
	}
	if r.minimumConfidence > 0 && answer.Confidence < r.minimumConfidence {
		return "", usage, MissLowConfidence, false
	}
	return answer.Choice, usage, "", true
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
