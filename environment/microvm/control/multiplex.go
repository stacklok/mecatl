package control

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ServiceName identifies a negotiated guest data-plane service.
type ServiceName string

const (
	// ServiceWorkspace selects version-aware filesystem operations.
	ServiceWorkspace ServiceName = "workspace"
	// ServiceExec selects bounded command execution and output streaming.
	ServiceExec ServiceName = "exec"
)

type multiplexHello struct {
	Version         uint16        `json:"version"`
	Binding         Binding       `json:"binding"`
	Capability      string        `json:"capability"`
	Services        []ServiceName `json:"services"`
	Capabilities    Capabilities  `json:"capabilities"`
	MaxMessageBytes uint32        `json:"max_message_bytes"`
}

type multiplexReply struct {
	Agreement Agreement     `json:"agreement"`
	Services  []ServiceName `json:"services"`
	ErrorCode string        `json:"error_code,omitempty"`
}

type frameKind string

const (
	kindRequest frameKind = "request"
	kindStream  frameKind = "stream"
	kindEnd     frameKind = "end"
	kindCancel  frameKind = "cancel"
	kindError   frameKind = "error"
)

type multiplexFrame struct {
	Kind       frameKind       `json:"kind"`
	Service    ServiceName     `json:"service,omitempty"`
	Method     string          `json:"method,omitempty"`
	RequestID  uint64          `json:"request_id"`
	Capability string          `json:"capability,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	ErrorCode  string          `json:"error_code,omitempty"`
}

// Client multiplexes concurrent workspace and exec requests over one authenticated stream.
type Client struct {
	stream io.ReadWriteCloser
	codec  Codec

	writeMu    sync.Mutex
	requestMu  sync.Mutex
	mu         sync.Mutex
	capability string
	binding    Binding
	agreement  Agreement
	pending    map[uint64]chan multiplexFrame
	nextID     atomic.Uint64
	done       chan struct{}
	err        error
	once       sync.Once
}

// OpenClient authenticates one host stream and negotiates all requested services once.
func OpenClient(ctx context.Context, stream io.ReadWriteCloser, binding Binding, capability string, services []ServiceName, maxMessageBytes uint32) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stream == nil || binding.validate() != nil || capability == "" || len(services) == 0 {
		return nil, ErrUnauthenticatedCapability
	}
	if maxMessageBytes == 0 || maxMessageBytes > DefaultMaxMessageBytes {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	codec := NewCodec(DefaultMaxMessageBytes)
	hello := multiplexHello{
		Version: ProtocolVersion, Binding: binding, Capability: capability,
		Services: append([]ServiceName(nil), services...), Capabilities: RequiredCapabilities(),
		MaxMessageBytes: maxMessageBytes,
	}
	if err := codec.Write(stream, hello); err != nil {
		return nil, err
	}
	var reply multiplexReply
	if err := codec.Read(stream, &reply); err != nil {
		return nil, err
	}
	if reply.ErrorCode != "" {
		switch reply.ErrorCode {
		case "unauthenticated":
			return nil, ErrUnauthenticatedCapability
		case "protocol_version":
			return nil, ErrProtocolVersion
		case "message_bound":
			return nil, ErrMessageBound
		case "capability_mismatch":
			return nil, ErrCapabilityMismatch
		default:
			return nil, fmt.Errorf("microvm guest handshake rejected: %s", reply.ErrorCode)
		}
	}
	if err := validateAgreement(reply.Agreement, hello.Version, hello.Capabilities, hello.MaxMessageBytes); err != nil {
		return nil, err
	}
	if !sameServices(reply.Services, services) {
		return nil, ErrCapabilityMismatch
	}
	client := &Client{
		stream: stream, codec: NewCodec(reply.Agreement.MaxMessageBytes), capability: capabilityProof(capability), binding: binding,
		agreement: reply.Agreement, pending: make(map[uint64]chan multiplexFrame), done: make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func sameServices(got, want []ServiceName) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[ServiceName]struct{}, len(got))
	for _, service := range got {
		set[service] = struct{}{}
	}
	for _, service := range want {
		if _, ok := set[service]; !ok {
			return false
		}
	}
	return true
}

func capabilityProof(capability string) string {
	digest := sha256.Sum256([]byte(capability))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

// Agreement returns a copy of the services/capabilities negotiated by the production handshake.
func (c *Client) Agreement() Agreement {
	if c == nil {
		return Agreement{}
	}
	agreement := c.agreement
	agreement.Capabilities = append(Capabilities(nil), agreement.Capabilities...)
	return agreement
}

// IsBoundTo reports whether the authenticated connection owns binding.
func (c *Client) IsBoundTo(binding Binding) bool {
	return c != nil && c.binding == binding
}

// Call performs one unary multiplexed request.
func (c *Client) Call(ctx context.Context, service ServiceName, method string, request, response any) error {
	return c.Stream(ctx, service, method, request, nil, response)
}

// Stream performs one request, delivering ordered stream payloads before the final response.
func (c *Client) Stream(ctx context.Context, service ServiceName, method string, request any, receive func(json.RawMessage) error, response any) error { //nolint:gocyclo // explicit request stream/cancel state machine
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode microvm multiplex request: %w", err)
	}
	c.requestMu.Lock()
	id := c.nextID.Add(1)
	responses := make(chan multiplexFrame, 1)
	c.mu.Lock()
	if c.err != nil {
		err = c.err
		c.mu.Unlock()
		c.requestMu.Unlock()
		return err
	}
	c.pending[id] = responses
	c.mu.Unlock()
	writeErr := c.write(multiplexFrame{
		Kind: kindRequest, Service: service, Method: method, RequestID: id,
		Capability: c.capability, Payload: payload,
	})
	c.requestMu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if writeErr != nil {
		return writeErr
	}
	ctxDone := ctx.Done()
	cancelled := false
	for {
		select {
		case <-ctxDone:
			if err := c.write(multiplexFrame{Kind: kindCancel, Service: service, Method: method, RequestID: id, Capability: c.capability}); err != nil {
				return ctx.Err()
			}
			cancelled = true
			ctxDone = nil
		case <-c.done:
			if cancelled {
				return ctx.Err()
			}
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return err
		case frame := <-responses:
			if frame.Service != service || frame.Method != method {
				return ErrMalformedFrame
			}
			switch frame.Kind {
			case kindStream:
				if receive != nil {
					if err := receive(frame.Payload); err != nil {
						_ = c.write(multiplexFrame{Kind: kindCancel, Service: service, Method: method, RequestID: id, Capability: c.capability})
						return err
					}
				}
			case kindEnd:
				if cancelled {
					return ctx.Err()
				}
				if response != nil && len(frame.Payload) != 0 {
					if err := json.Unmarshal(frame.Payload, response); err != nil {
						return fmt.Errorf("decode microvm multiplex response: %w", err)
					}
				}
				return nil
			case kindError:
				return &RemoteError{Code: frame.ErrorCode}
			default:
				return ErrMalformedFrame
			}
		}
	}
}

func (c *Client) write(frame multiplexFrame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.codec.Write(c.stream, frame); err != nil {
		wrapped := fmt.Errorf("write microvm multiplex frame: %w", err)
		if !errors.Is(err, ErrFrameTooLarge) && !errors.Is(err, ErrMalformedFrame) {
			c.fail(wrapped)
		}
		return wrapped
	}
	return nil
}

func (c *Client) readLoop() {
	for {
		var frame multiplexFrame
		if err := c.codec.Read(c.stream, &frame); err != nil {
			c.fail(err)
			return
		}
		c.mu.Lock()
		responses := c.pending[frame.RequestID]
		c.mu.Unlock()
		if responses != nil {
			select {
			case responses <- frame:
			case <-c.done:
				return
			}
		}
	}
}

func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

// Close closes the sole guest stream and unblocks all requests.
func (c *Client) Close() error {
	err := c.stream.Close()
	c.fail(io.EOF)
	return err
}

// RemoteError is a bounded service error code returned by the guest.
type RemoteError struct{ Code string }

func (e *RemoteError) Error() string { return "microvm guest service error: " + e.Code }

// Handler serves one negotiated multiplex service.
type Handler func(context.Context, string, json.RawMessage, func(any) error) (any, string, error)

// ServeMultiplex authenticates one capability, negotiates services, and dispatches request IDs.
func ServeMultiplex(ctx context.Context, stream io.ReadWriteCloser, expected Binding, verifier *CapabilityVerifier, handlers map[ServiceName]Handler, maxMessageBytes uint32) error {
	return serveMultiplex(ctx, stream, verifier, maxMessageBytes, func(claim Binding) (Binding, map[ServiceName]Handler, bool) {
		return expected, handlers, claim == expected
	})
}

// RegisteredHandlerResolver resolves one complete logical binding to its assigned-root handlers.
type RegisteredHandlerResolver func(Binding) (map[ServiceName]Handler, bool)

// ServeRegisteredMultiplex authenticates a logical binding before selecting any
// assigned-root handler. Each connection remains bound to that exact tuple.
func ServeRegisteredMultiplex(ctx context.Context, stream io.ReadWriteCloser, verifier *CapabilityVerifier, resolve RegisteredHandlerResolver, maxMessageBytes uint32) error {
	if resolve == nil {
		return ErrUnauthenticatedCapability
	}
	return serveMultiplex(ctx, stream, verifier, maxMessageBytes, func(claim Binding) (Binding, map[ServiceName]Handler, bool) {
		handlers, ok := resolve(claim)
		return claim, handlers, ok
	})
}

func serveMultiplex(ctx context.Context, stream io.ReadWriteCloser, verifier *CapabilityVerifier, maxMessageBytes uint32, resolve func(Binding) (Binding, map[ServiceName]Handler, bool)) error { //nolint:gocyclo // explicit bounded connection state machine
	if stream == nil || verifier == nil || resolve == nil {
		return ErrUnauthenticatedCapability
	}
	if maxMessageBytes == 0 || maxMessageBytes > DefaultMaxMessageBytes {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	handshakeCodec := NewCodec(DefaultMaxMessageBytes)
	var hello multiplexHello
	if err := handshakeCodec.Read(stream, &hello); err != nil {
		return err
	}
	expected, handlers, registered := resolve(hello.Binding)
	agreement := Agreement{
		Version: ProtocolVersion, Capabilities: RequiredCapabilities(),
		MaxMessageBytes: min(hello.MaxMessageBytes, maxMessageBytes),
	}
	reply := multiplexReply{Agreement: agreement, Services: append([]ServiceName(nil), hello.Services...)}
	var negotiationErr error
	switch {
	case hello.Version != ProtocolVersion:
		negotiationErr = ErrProtocolVersion
		reply.ErrorCode = "protocol_version"
	case !registered || expected.validate() != nil || hello.Binding != expected:
		negotiationErr = ErrUnauthenticatedCapability
		reply.ErrorCode = "unauthenticated"
	case hello.MaxMessageBytes == 0:
		negotiationErr = ErrMessageBound
		reply.ErrorCode = "message_bound"
	case !servicesAvailable(hello.Services, handlers):
		negotiationErr = ErrCapabilityMismatch
		reply.ErrorCode = "capability_mismatch"
	case validateAgreement(agreement, hello.Version, hello.Capabilities, hello.MaxMessageBytes) != nil:
		negotiationErr = ErrCapabilityMismatch
		reply.ErrorCode = "capability_mismatch"
	case verifier.Verify(hello.Capability, expected) != nil:
		negotiationErr = ErrUnauthenticatedCapability
		reply.ErrorCode = "unauthenticated"
	}
	if err := handshakeCodec.Write(stream, reply); err != nil {
		return err
	}
	if negotiationErr != nil {
		return negotiationErr
	}
	codec := NewCodec(reply.Agreement.MaxMessageBytes)
	connectionCtx, stop := context.WithCancel(ctx)
	var writeMu sync.Mutex
	write := func(frame multiplexFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return codec.Write(stream, frame)
	}
	var requestsMu sync.Mutex
	requests := make(map[uint64]context.CancelFunc)
	var handlersWG sync.WaitGroup
	var highestRequestID uint64
	defer func() {
		stop()
		requestsMu.Lock()
		cancels := make([]context.CancelFunc, 0, len(requests))
		for _, cancel := range requests {
			cancels = append(cancels, cancel)
		}
		requestsMu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		handlersWG.Wait()
	}()
	for {
		var frame multiplexFrame
		if err := codec.Read(stream, &frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return io.EOF
			}
			return err
		}
		switch frame.Kind {
		case kindCancel:
			if !hmac.Equal([]byte(frame.Capability), []byte(capabilityProof(hello.Capability))) {
				return ErrUnauthenticatedCapability
			}
			requestsMu.Lock()
			cancel := requests[frame.RequestID]
			requestsMu.Unlock()
			if cancel != nil {
				cancel()
			}
		case kindRequest:
			if !hmac.Equal([]byte(frame.Capability), []byte(capabilityProof(hello.Capability))) {
				return ErrUnauthenticatedCapability
			}
			if frame.RequestID == 0 || frame.RequestID <= highestRequestID || frame.Method == "" {
				return ErrMalformedFrame
			}
			highestRequestID = frame.RequestID
			handler := handlers[frame.Service]
			if handler == nil {
				if err := write(multiplexFrame{Kind: kindError, Service: frame.Service, Method: frame.Method, RequestID: frame.RequestID, ErrorCode: "unsupported_service"}); err != nil {
					return err
				}
				continue
			}
			requestCtx, cancel := context.WithCancel(connectionCtx)
			requestsMu.Lock()
			if _, duplicate := requests[frame.RequestID]; duplicate {
				requestsMu.Unlock()
				cancel()
				return ErrMalformedFrame
			}
			requests[frame.RequestID] = cancel
			requestsMu.Unlock()
			handlersWG.Add(1)
			go func(frame multiplexFrame) {
				defer handlersWG.Done()
				defer func() {
					cancel()
					requestsMu.Lock()
					delete(requests, frame.RequestID)
					requestsMu.Unlock()
				}()
				send := func(value any) error {
					payload, err := json.Marshal(value)
					if err != nil {
						return err
					}
					return write(multiplexFrame{Kind: kindStream, Service: frame.Service, Method: frame.Method, RequestID: frame.RequestID, Payload: payload})
				}
				final, code, err := handler(requestCtx, frame.Method, frame.Payload, send)
				if err != nil {
					if code == "" {
						code = "internal"
					}
					_ = write(multiplexFrame{Kind: kindError, Service: frame.Service, Method: frame.Method, RequestID: frame.RequestID, ErrorCode: code})
					return
				}
				payload, marshalErr := json.Marshal(final)
				if marshalErr != nil {
					_ = write(multiplexFrame{Kind: kindError, Service: frame.Service, Method: frame.Method, RequestID: frame.RequestID, ErrorCode: "internal"})
					return
				}
				_ = write(multiplexFrame{Kind: kindEnd, Service: frame.Service, Method: frame.Method, RequestID: frame.RequestID, Payload: payload})
			}(frame)
		default:
			return ErrMalformedFrame
		}
	}
}

func servicesAvailable(services []ServiceName, handlers map[ServiceName]Handler) bool {
	if len(services) == 0 {
		return false
	}
	seen := make(map[ServiceName]struct{}, len(services))
	for _, service := range services {
		if handlers[service] == nil {
			return false
		}
		if _, duplicate := seen[service]; duplicate {
			return false
		}
		seen[service] = struct{}{}
	}
	return true
}
