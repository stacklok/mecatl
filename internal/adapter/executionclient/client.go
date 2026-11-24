// Package executionclient implements the private mTLS client and placement adapter
// for the independently deployed Kubernetes execution provider.
//
//nolint:revive // Private protocol methods are intentionally explicit across adapter boundaries.
package executionclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/creatediag"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

const (
	remoteRoot                     = "/workspace"
	authorityResolveRequestTimeout = 10 * time.Second
)

// TLSFiles names the runtime-only mTLS material mounted into mecak8s.
// TLSFiles names a complete mTLS projection. ServerName explicitly pins the
// verified DNS identity when the dial address is a local port-forward; empty
// uses the endpoint hostname for TLS verification.
type TLSFiles struct{ CA, Cert, Key, ServerName string }

// LoadTLSConfig loads provider trust and client identity from a complete projection.
func LoadTLSConfig(files TLSFiles) (*tls.Config, error) {
	cfg, _, err := loadTLSMaterial(files)
	return cfg, err
}

func loadTLSMaterial(files TLSFiles) (*tls.Config, [32]byte, error) {
	var digest [32]byte
	if files.CA == "" || files.Cert == "" || files.Key == "" {
		return nil, digest, errors.New("execution client: CA, certificate, and key are required")
	}
	ca, err := os.ReadFile(files.CA)
	if err != nil {
		return nil, digest, fmt.Errorf("execution client: load CA: %w", err)
	}
	certPEM, err := os.ReadFile(files.Cert)
	if err != nil {
		return nil, digest, fmt.Errorf("execution client: load certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(files.Key)
	if err != nil {
		return nil, digest, fmt.Errorf("execution client: load key: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, digest, errors.New("execution client: CA contains no certificates")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, digest, fmt.Errorf("execution client: load client identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) || !clientAuthUsage(leaf.ExtKeyUsage) {
		return nil, digest, errors.New("execution client: client certificate is not valid for client authentication")
	}
	cert.Leaf = leaf
	h := sha256.New()
	for _, part := range [][]byte{ca, certPEM, keyPEM} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(part)
	}
	copy(digest[:], h.Sum(nil))
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: files.ServerName}, digest, nil
}

func clientAuthUsage(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

// Client is a bounded client for the private execution-provider gRPC API.
type Client struct {
	conn     atomic.Pointer[grpc.ClientConn]
	rpc      executionv1.ExecutionProviderServiceClient
	mu       sync.Mutex
	active   map[*grpc.ClientConn]int
	retired  map[*grpc.ClientConn]*time.Timer
	files    TLSFiles
	endpoint string
	material [32]byte
	identity string
	trust    *x509.CertPool
	expires  time.Time
	closed   bool
}

func (c *Client) acquire() (*grpc.ClientConn, func(), error) {
	c.mu.Lock()
	conn := c.conn.Load()
	if conn == nil {
		c.mu.Unlock()
		return nil, nil, status.Error(codes.Unavailable, "execution client: TLS material unavailable")
	}
	if c.active == nil {
		c.active = make(map[*grpc.ClientConn]int)
	}
	c.active[conn]++
	c.mu.Unlock()
	return conn, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.active[conn]--
		if c.active[conn] == 0 {
			delete(c.active, conn)
			if timer, ok := c.retired[conn]; ok {
				timer.Stop()
				delete(c.retired, conn)
				_ = conn.Close()
			}
		}
	}, nil
}

// Invoke pins the transport for the complete RPC, including its terminal reply.
func (c *Client) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	conn, release, err := c.acquire()
	if err != nil {
		return err
	}
	defer release()
	return conn.Invoke(ctx, method, args, reply, opts...)
}

func (c *Client) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	conn, release, err := c.acquire()
	if err != nil {
		return nil, err
	}
	stream, err := conn.NewStream(ctx, desc, method, opts...)
	if err != nil {
		release()
		return nil, err
	}
	pinned := &pinnedStream{ClientStream: stream, release: release, done: make(chan struct{})}
	go func() {
		select {
		case <-stream.Context().Done():
			pinned.finish()
		case <-pinned.done:
		}
	}()
	return pinned, nil
}

type pinnedStream struct {
	grpc.ClientStream
	release func()
	once    sync.Once
	done    chan struct{}
}

func (s *pinnedStream) finish() {
	s.once.Do(func() { s.release(); close(s.done) })
}

func (s *pinnedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.finish()
	}
	return err
}

func (s *pinnedStream) SendMsg(m any) error {
	err := s.ClientStream.SendMsg(m)
	if err != nil {
		s.finish()
	}
	return err
}

// New constructs a production mTLS execution-provider client. Endpoint is a
// plain host:port authority; URI schemes and alternate resolvers are rejected.
func New(endpoint string, tlsConfig *tls.Config) (*Client, error) {
	if strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") {
		return nil, errors.New("execution client: endpoint must be host:port")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("execution client: endpoint must be host:port")
	}
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 || tlsConfig.RootCAs == nil {
		return nil, errors.New("execution client: production mTLS configuration is required")
	}
	cfg := tlsConfig.Clone()
	if cfg.MinVersion < tls.VersionTLS13 {
		cfg.MinVersion = tls.VersionTLS13
	}
	conn, err := dial(endpoint, cfg)
	if err != nil {
		return nil, err
	}
	leaf := cfg.Certificates[0].Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("execution client: parse client certificate: %w", err)
		}
	}
	c := &Client{trust: cfg.RootCAs, expires: leaf.NotAfter}
	if len(leaf.URIs) == 1 {
		c.identity = leaf.URIs[0].String()
	}
	c.conn.Store(conn)
	c.rpc = executionv1.NewExecutionProviderServiceClient(c)
	return c, nil
}

func dial(endpoint string, cfg *tls.Config) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(grpccredentials.NewTLS(cfg)),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(executionenv.MaxMessageBytes), grpc.MaxCallSendMsgSize(executionenv.MaxMessageBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("execution client: new client: %w", err)
	}
	return conn, nil
}

// NewWithTLSFiles owns a reloadable connection; invalid material withdraws
// active authority instead of allowing a stale channel to continue.
func NewWithTLSFiles(endpoint string, files TLSFiles) (*Client, error) {
	cfg, digest, err := loadTLSMaterial(files)
	if err != nil {
		return nil, err
	}
	c, err := New(endpoint, cfg)
	if err != nil {
		return nil, err
	}
	c.files, c.endpoint, c.material = files, endpoint, digest
	return c, nil
}

func projectedGeneration(files TLSFiles) string {
	if filepath.Dir(files.Cert) != filepath.Dir(files.Key) {
		return ""
	}
	generation, _ := os.Readlink(filepath.Join(filepath.Dir(files.Cert), "..data"))
	return generation
}

// ReloadTLS publishes a complete, stable identity/trust bundle. Only same-identity
// renewal under unchanged trust may drain existing RPCs; withdrawal closes them.
//
//nolint:gocyclo // Projection validation and connection swap must remain one locked transition.
func (c *Client) ReloadTLS() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.endpoint == "" {
		return errors.New("execution client: reload is unavailable")
	}
	var cfg *tls.Config
	var digest [32]byte
	var err error
	projectionChanging := false
	// Kubernetes ..data projections can switch between two individual reads.
	// Require two consecutive valid identical bundles before publishing either.
	for i := 0; i < 3; i++ {
		projectionChanging = false
		before := projectedGeneration(c.files)
		cfg, digest, err = loadTLSMaterial(c.files)
		after := projectedGeneration(c.files)
		if before != after && before != "" {
			projectionChanging = true
			err = errors.New("execution client: TLS projection switched during read")
		} else if err == nil {
			_, next, nextErr := loadTLSMaterial(c.files)
			if projectedGeneration(c.files) != after && after != "" {
				projectionChanging = true
				err = errors.New("execution client: TLS projection switched during validation")
			} else if nextErr == nil && next == digest {
				break
			} else {
				err = nextErr
				if err == nil {
					err = errors.New("execution client: TLS projection changed during read")
				}
			}
		}
		if i < 2 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err != nil && projectionChanging && c.conn.Load() != nil && time.Now().Before(c.expires) {
		return err
	}
	if err == nil && c.conn.Load() != nil && digest == c.material {
		if !time.Now().Before(c.expires) {
			c.withdraw()
			return errors.New("execution client: client certificate expired")
		}
		return nil
	}
	if err != nil {
		c.withdraw()
		return err
	}
	conn, err := dial(c.endpoint, cfg)
	if err != nil {
		c.withdraw()
		return err
	}
	leaf := cfg.Certificates[0].Leaf
	identity := ""
	if len(leaf.URIs) == 1 {
		identity = leaf.URIs[0].String()
	}
	drain := identity != "" && identity == c.identity && c.trust != nil && c.trust.Equal(cfg.RootCAs) && time.Now().Before(c.expires)
	old := c.conn.Swap(conn)
	if old != nil {
		if drain && c.active[old] > 0 {
			if c.retired == nil {
				c.retired = make(map[*grpc.ClientConn]*time.Timer)
			}
			c.retired[old] = time.AfterFunc(time.Until(c.expires), func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if _, ok := c.retired[old]; ok {
					delete(c.retired, old)
					_ = old.Close()
				}
			})
		} else {
			_ = old.Close()
		}
	}
	if !drain {
		c.closeRetired()
	}
	c.material, c.identity, c.trust, c.expires = digest, identity, cfg.RootCAs, leaf.NotAfter
	return nil
}

// withdraw and closeRetired require c.mu.
func (c *Client) withdraw() {
	if old := c.conn.Swap(nil); old != nil {
		_ = old.Close()
	}
	c.closeRetired()
}

func (c *Client) closeRetired() {
	for conn, timer := range c.retired {
		timer.Stop()
		_ = conn.Close()
		delete(c.retired, conn)
	}
}

// RunTLSReload checks projected credentials until shutdown; renewal is not
// independent of security-policy publication.
func (c *Client) RunTLSReload(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.ReloadTLS()
		}
	}
}

// Close releases the provider connection.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.withdraw()
}

func newOperationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func bindOperationID(owner executionenv.Owner, binding, profile string) string {
	sum := sha256.Sum256([]byte("bind/v1\x00" + owner.Issuer + "\x00" + owner.Subject + "\x00" + binding + "\x00" + profile))
	return "bind-v1-" + hex.EncodeToString(sum[:])
}

func (c *Client) EnsureTemplate(ctx context.Context, binding, id, revision string, owner executionenv.Owner, operationID string) (executionenv.EnsureEnvironmentResponse, error) {
	return c.EnsureTemplateCapabilities(ctx, binding, id, revision, owner, operationID, nil)
}

// EnsureTemplateCapabilities is EnsureTemplate with a capability request. A
// refusal keeps the provider's message, which the provider limits to safe text.
func (c *Client) EnsureTemplateCapabilities(
	ctx context.Context,
	binding string,
	id string,
	revision string,
	owner executionenv.Owner,
	operationID string,
	capabilities []string,
) (executionenv.EnsureEnvironmentResponse, error) {
	v, err := c.rpc.EnsureTemplate(ctx, &executionv1.EnsureTemplateRequest{
		BindingId:    binding,
		Template:     &executionv1.TemplateSelector{Id: id, Revision: revision},
		Owner:        ownerToProto(owner),
		OperationId:  operationID,
		Capabilities: capabilities,
	})
	if err != nil {
		return executionenv.EnsureEnvironmentResponse{}, decodeAdmissionError(ctx, err)
	}
	return ensureFromProto(v)
}

func decodeAdmissionError(ctx context.Context, err error) error {
	decoded := decodeError(ctx, err)
	var remote *executionenv.Error
	if !errors.As(decoded, &remote) {
		return decoded
	}
	switch remote.Code {
	case executionenv.CodeInvalidArgument,
		executionenv.CodePermissionDenied,
		executionenv.CodeConflict:
	default:
		return decoded
	}
	message := status.Convert(err).Message()
	if message == genericProviderMessage || len(message) > 256 || !utf8.ValidString(message) ||
		strings.IndexFunc(message, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return decoded
	}
	return &executionenv.Error{Code: remote.Code, Message: message}
}

// ListExecutionTemplates returns only operator-authorized display projections.
func (c *Client) ListExecutionTemplates(ctx context.Context) (*executionv1.ListExecutionTemplatesResponse, error) {
	v, err := c.rpc.ListExecutionTemplates(ctx, &executionv1.ListExecutionTemplatesRequest{})
	return v, decodeError(ctx, err)
}

// ValidateTemplate checks an exact eligible revision without allocation.
func (c *Client) ValidateTemplate(ctx context.Context, id, revision string) (*executionv1.ValidateTemplateResponse, error) {
	v, err := c.rpc.ValidateTemplate(ctx, &executionv1.ValidateTemplateRequest{Template: &executionv1.TemplateSelector{Id: id, Revision: revision}})
	return v, decodeError(ctx, err)
}

// Attach obtains current allocation state for an exact environment reference.
func (c *Client) Attach(ctx context.Context, req executionenv.AttachEnvironmentRequest) (executionenv.AttachEnvironmentResponse, error) {
	v, err := c.rpc.AttachEnvironment(ctx, &executionv1.AttachEnvironmentRequest{Context: contextToProto(req.Context), Purpose: req.Purpose})
	if err != nil {
		return executionenv.AttachEnvironmentResponse{}, decodeError(ctx, err)
	}
	return attachFromProto(v)
}

// AcquireRun acquires one environment-wide run claim.
func (c *Client) AcquireRun(ctx context.Context, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	v, err := c.rpc.AcquireRun(ctx, &executionv1.AcquireRunRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), BindingId: req.BindingID, RunId: req.RunID, OperationId: req.OperationID, TtlMillis: req.TTL.Milliseconds()})
	if err != nil {
		return executionenv.RunClaim{}, decodeError(ctx, err)
	}
	return runClaimFromProto(v)
}
func (c *Client) RenewRun(ctx context.Context, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	v, err := c.rpc.RenewRun(ctx, &executionv1.RenewRunRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), BindingId: req.BindingID, RunId: req.RunID, ClaimId: req.ClaimID, Epoch: req.Epoch, GrantGeneration: req.GrantGeneration, OperationId: req.OperationID, TtlMillis: req.TTL.Milliseconds()})
	if err != nil {
		return executionenv.RunClaim{}, decodeError(ctx, err)
	}
	return runClaimFromProto(v)
}
func (c *Client) ReleaseRun(ctx context.Context, req executionenv.RunClaimRequest) error {
	_, err := c.rpc.ReleaseRun(ctx, &executionv1.ReleaseRunRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), BindingId: req.BindingID, RunId: req.RunID, ClaimId: req.ClaimID, Epoch: req.Epoch, GrantGeneration: req.GrantGeneration, OperationId: req.OperationID})
	return decodeError(ctx, err)
}
func (c *Client) referenceMutation(ctx context.Context, req executionenv.ReferenceRequest, call func(context.Context, *executionv1.ReferenceMutationRequest, ...grpc.CallOption) (*emptypb.Empty, error)) error {
	_, err := call(ctx, &executionv1.ReferenceMutationRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), BindingId: req.BindingID, OperationId: req.OperationID})
	return decodeError(ctx, err)
}
func (c *Client) CommitReference(ctx context.Context, req executionenv.ReferenceRequest) error {
	return c.referenceMutation(ctx, req, c.rpc.CommitReference)
}
func (c *Client) AbortReference(ctx context.Context, req executionenv.ReferenceRequest) error {
	return c.referenceMutation(ctx, req, c.rpc.AbortReference)
}
func (c *Client) PrepareReferenceDelete(ctx context.Context, req executionenv.ReferenceRequest) error {
	return c.referenceMutation(ctx, req, c.rpc.PrepareReferenceDelete)
}
func (c *Client) ConfirmReferenceDelete(ctx context.Context, req executionenv.ReferenceRequest) error {
	return c.referenceMutation(ctx, req, c.rpc.ConfirmReferenceDelete)
}
func (c *Client) CancelReferenceDelete(ctx context.Context, req executionenv.ReferenceRequest) error {
	return c.referenceMutation(ctx, req, c.rpc.CancelReferenceDelete)
}

// ReserveSuccessor reserves a successor reference and returns the admitted
// set it inherits from the source environment.
func (c *Client) ReserveSuccessor(
	ctx context.Context, req executionenv.ReferenceRequest,
) ([]string, error) {
	v, err := c.rpc.ReserveSuccessor(ctx, &executionv1.ReserveSuccessorRequest{
		Environment:          refToProto(req.Environment),
		Owner:                ownerToProto(req.Owner),
		SourceBindingId:      req.SourceBindingID,
		DestinationBindingId: req.BindingID,
		OperationId:          req.OperationID,
	})
	if err != nil {
		return nil, decodeError(ctx, err)
	}
	if !executionenv.ValidAdmittedCapabilities(v.GetAdmittedCapabilities()) {
		return nil, sanitizedProviderError()
	}
	return v.GetAdmittedCapabilities(), nil
}

func (c *Client) ListReferenceIntents(ctx context.Context, owner executionenv.Owner) ([]executionenv.ReferenceIntent, error) {
	return c.listReferenceIntents(ctx, &executionv1.ListReferenceIntentsRequest{Owner: ownerToProto(owner), Limit: 64})
}

func (c *Client) FindReferenceIntent(ctx context.Context, owner executionenv.Owner, environment executionenv.EnvironmentRef, binding string) (executionenv.ReferenceIntent, error) {
	intents, err := c.listReferenceIntents(ctx, &executionv1.ListReferenceIntentsRequest{Owner: ownerToProto(owner), Limit: 1, Environment: refToProto(environment), BindingId: binding})
	if err != nil {
		return executionenv.ReferenceIntent{}, err
	}
	if len(intents) != 1 {
		return executionenv.ReferenceIntent{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference intent not found"}
	}
	return intents[0], nil
}

func (c *Client) ListAllReferenceIntents(ctx context.Context) ([]executionenv.ReferenceIntent, error) {
	return c.listReferenceIntents(ctx, &executionv1.ListReferenceIntentsRequest{Limit: 64})
}

func (c *Client) listReferenceIntents(ctx context.Context, req *executionv1.ListReferenceIntentsRequest) ([]executionenv.ReferenceIntent, error) {
	v, err := c.rpc.ListReferenceIntents(ctx, req)
	if err != nil {
		return nil, decodeError(ctx, err)
	}
	out := make([]executionenv.ReferenceIntent, 0, len(v.GetIntents()))
	for _, intent := range v.GetIntents() {
		created, timeErr := checkedTime(intent.GetCreatedAt())
		if timeErr != nil {
			return nil, sanitizedProviderError()
		}
		state := executionenv.ReferenceState(intent.GetState())
		if state != executionenv.ReferencePendingCreate && state != executionenv.ReferencePendingDelete {
			return nil, sanitizedProviderError()
		}
		owner := executionenv.Owner{}
		ownerOK := intent.GetOwner() == nil
		if intent.GetOwner() != nil {
			owner = executionenv.Owner{Issuer: intent.GetOwner().GetIssuer(), Subject: intent.GetOwner().GetSubject()}
			ownerOK = owner.Issuer != "" && owner.Subject != "" && len(owner.Issuer) <= executionenv.MaxIdentityBytes && len(owner.Subject) <= executionenv.MaxIdentityBytes
		}
		if !ownerOK {
			return nil, sanitizedProviderError()
		}
		out = append(out, executionenv.ReferenceIntent{Environment: refFromProto(intent.GetEnvironment()), Owner: owner, BindingID: intent.GetBindingId(), State: state, OperationID: intent.GetOperationId(), SourceBindingID: intent.GetSourceBindingId(), CreatedAt: created})
	}
	return out, nil
}

// ReleaseReference releases one durable binding reference.
func (c *Client) ReleaseReference(ctx context.Context, req executionenv.ReferenceReleaseRequest) error {
	_, err := c.rpc.ReleaseReference(ctx, &executionv1.ReleaseReferenceRequest{Context: contextToProto(req.Context)})
	return decodeError(ctx, err)
}

// ReplaceExecutor requests UID-checked administrative executor replacement.
func (c *Client) ReplaceExecutor(ctx context.Context, req executionenv.RetireEnvironmentRequest) error {
	_, err := c.rpc.ReplaceExecutor(ctx, &executionv1.ReplaceExecutorRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), ExpectedExecutionEpoch: req.ExpectedEpoch, ExpectedPodUid: req.ExpectedPodUID, ExpectedPvcUid: req.ExpectedPVCUID, OperationId: req.OperationID})
	return decodeError(ctx, err)
}

// RetireEnvironment requests administrative retirement.
func (c *Client) RetireEnvironment(ctx context.Context, req executionenv.RetireEnvironmentRequest) error {
	_, err := c.rpc.RetireEnvironment(ctx, &executionv1.RetireEnvironmentRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), ExpectedExecutionEpoch: req.ExpectedEpoch, ExpectedPodUid: req.ExpectedPodUID, ExpectedPvcUid: req.ExpectedPVCUID, OperationId: req.OperationID})
	return decodeError(ctx, err)
}

// DeleteRetiredEnvironment deletes retained storage after retirement proof.
func (c *Client) DeleteRetiredEnvironment(ctx context.Context, ref executionenv.EnvironmentRef, owner executionenv.Owner, expectedPVCUID, operationID string) error {
	_, err := c.rpc.DeleteRetiredEnvironment(ctx, &executionv1.DeleteRetiredEnvironmentRequest{Environment: refToProto(ref), Owner: ownerToProto(owner), ExpectedPvcUid: expectedPVCUID, OperationId: operationID})
	return decodeError(ctx, err)
}

// RevokeEnvironment atomically fences active runs and replays a matching operation receipt.
func (c *Client) RevokeEnvironment(ctx context.Context, ref executionenv.EnvironmentRef, owner executionenv.Owner, expectedGeneration uint64, operationID string) (uint64, error) {
	out, err := c.rpc.RevokeEnvironment(ctx, &executionv1.RevokeEnvironmentRequest{Environment: refToProto(ref), Owner: ownerToProto(owner), ExpectedGrantGeneration: expectedGeneration, OperationId: operationID})
	if err != nil {
		return 0, decodeError(ctx, err)
	}
	return out.GetGrantGeneration(), nil
}

// RecoverEnvironment clears a fence only after the provider verifies exact terminal evidence.
func (c *Client) RecoverEnvironment(ctx context.Context, req executionenv.RetireEnvironmentRequest) error {
	_, err := c.rpc.RecoverEnvironment(ctx, &executionv1.RecoverEnvironmentRequest{Environment: refToProto(req.Environment), Owner: ownerToProto(req.Owner), ExpectedExecutionEpoch: req.ExpectedEpoch, ExpectedPodUid: req.ExpectedPodUID, ExpectedPvcUid: req.ExpectedPVCUID, OperationId: req.OperationID})
	return decodeError(ctx, err)
}

func decodeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	st, ok := status.FromError(err)
	if !ok || len(st.Details()) != 1 {
		return sanitizedProviderError()
	}
	detail, ok := st.Details()[0].(*executionv1.ErrorDetail)
	if !ok {
		return sanitizedProviderError()
	}
	code := executionenv.ErrorCode(detail.Code)
	if !code.Valid() || st.Code() != grpcCodeForError(code) || (detail.Retryable && code != executionenv.CodeNotReady && code != executionenv.CodeUnauthenticated) {
		return sanitizedProviderError()
	}
	return &executionenv.Error{
		Code:      code,
		Message:   genericProviderMessage,
		Retryable: detail.Retryable,
	}
}

const genericProviderMessage = "execution provider request failed"

func sanitizedProviderError() error {
	return &executionenv.Error{Code: executionenv.CodeInternal, Message: genericProviderMessage}
}

func grpcCodeForError(code executionenv.ErrorCode) codes.Code {
	switch code {
	case executionenv.CodeInvalidArgument:
		return codes.InvalidArgument
	case executionenv.CodeUnauthenticated:
		return codes.Unauthenticated
	case executionenv.CodePermissionDenied:
		return codes.PermissionDenied
	case executionenv.CodeNotFound:
		return codes.NotFound
	case executionenv.CodeAlreadyExists:
		return codes.AlreadyExists
	case executionenv.CodeConflict, executionenv.CodeVersionMismatch, executionenv.CodeDirectoryNotEmpty:
		return codes.Aborted
	case executionenv.CodeNotReady:
		return codes.Unavailable
	case executionenv.CodeFenceUnknown:
		return codes.FailedPrecondition
	case executionenv.CodeResourceExhausted:
		return codes.ResourceExhausted
	default:
		return codes.Internal
	}
}
func ownerToProto(o executionenv.Owner) *executionv1.Owner {
	return &executionv1.Owner{Issuer: o.Issuer, Subject: o.Subject}
}
func refToProto(r executionenv.EnvironmentRef) *executionv1.EnvironmentRef {
	return &executionv1.EnvironmentRef{Id: r.ID, Revision: r.Revision}
}
func contextToProto(v executionenv.RequestContext) *executionv1.RequestContext {
	return &executionv1.RequestContext{Environment: refToProto(v.Environment), Owner: ownerToProto(v.Owner), BindingId: v.BindingID, RunId: v.RunID, ClaimId: v.ClaimID, Epoch: v.Epoch, GrantGeneration: v.GrantGeneration}
}
func refFromProto(r *executionv1.EnvironmentRef) executionenv.EnvironmentRef {
	return executionenv.EnvironmentRef{ID: r.GetId(), Revision: r.GetRevision()}
}
func runClaimFromProto(v *executionv1.RunClaimResponse) (executionenv.RunClaim, error) {
	expiry, err := checkedTime(v.GetExpiresAt())
	if err != nil || v.GetClaimId() == "" || v.GetRunId() == "" || v.GetEpoch() == 0 || v.GetGrantGeneration() == 0 {
		return executionenv.RunClaim{}, sanitizedProviderError()
	}
	return executionenv.RunClaim{Environment: refFromProto(v.GetEnvironment()), BindingID: v.GetBindingId(), RunID: v.GetRunId(), ClaimID: v.GetClaimId(), Epoch: v.GetEpoch(), GrantGeneration: v.GetGrantGeneration(), ExpiresAt: expiry}, nil
}

func ensureFromProto(v *executionv1.EnsureEnvironmentResponse) (executionenv.EnsureEnvironmentResponse, error) {
	if v == nil {
		return executionenv.EnsureEnvironmentResponse{}, sanitizedProviderError()
	}
	if !executionenv.ValidAdmittedCapabilities(v.GetAdmittedCapabilities()) {
		return executionenv.EnsureEnvironmentResponse{}, sanitizedProviderError()
	}
	return executionenv.EnsureEnvironmentResponse{
		Environment:          refFromProto(v.GetEnvironment()),
		Epoch:                v.GetEpoch(),
		Ready:                v.GetReady(),
		GrantGeneration:      v.GetGrantGeneration(),
		AdmittedCapabilities: v.GetAdmittedCapabilities(),
	}, nil
}
func attachFromProto(v *executionv1.AttachEnvironmentResponse) (executionenv.AttachEnvironmentResponse, error) {
	if v == nil {
		return executionenv.AttachEnvironmentResponse{}, sanitizedProviderError()
	}
	if !executionenv.ValidAdmittedCapabilities(v.GetAdmittedCapabilities()) {
		return executionenv.AttachEnvironmentResponse{}, sanitizedProviderError()
	}
	return executionenv.AttachEnvironmentResponse{
		Environment:          refFromProto(v.GetEnvironment()),
		Epoch:                v.GetEpoch(),
		Ready:                v.GetReady(),
		GrantGeneration:      v.GetGrantGeneration(),
		AdmittedCapabilities: v.GetAdmittedCapabilities(),
	}, nil
}
func checkedTime(v interface {
	CheckValid() error
	AsTime() time.Time
}) (time.Time, error) {
	if v == nil {
		return time.Time{}, errors.New("execution provider omitted timestamp")
	}
	if err := v.CheckValid(); err != nil {
		return time.Time{}, errors.New("execution provider returned invalid timestamp")
	}
	return v.AsTime(), nil
}

// Provider adapts the execution service to server placement binding.
type Provider struct {
	client   *Client
	profile  string
	revision string
}

// NewTemplateProvider pins the deployment default to an exact execution revision.
func NewTemplateProvider(client *Client, id string, revision string) (*Provider, error) {
	if client == nil || id == "" || len(revision) != 67 || !strings.HasPrefix(revision, "v1-") || revision != strings.ToLower(revision) {
		return nil, errors.New("execution client: template ID and exact v1 revision are required")
	}
	if _, err := hex.DecodeString(revision[3:]); err != nil {
		return nil, errors.New("execution client: template ID and exact v1 revision are required")
	}
	return &Provider{client: client, profile: id, revision: revision}, nil
}

type referenceDeleteHandle struct {
	client *Client
	req    executionenv.ReferenceRequest
}

func (h *referenceDeleteHandle) Confirm(ctx context.Context) error {
	return h.client.ConfirmReferenceDelete(ctx, h.req)
}
func (h *referenceDeleteHandle) Cancel(ctx context.Context) error {
	return h.client.CancelReferenceDelete(ctx, h.req)
}
func (p *Provider) PrepareReferenceDelete(ctx context.Context, req server.PlacementSuccessorRequest) (server.ReferenceDeleteHandle, error) {
	if !p.Applies(req.Ref) || req.SourceBindingID == "" {
		return nil, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return nil, err
	}
	op := ""
	exact := executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}
	intent, err := p.client.FindReferenceIntent(ctx, owner, exact, string(req.SourceBindingID))
	if err == nil {
		if intent.State != executionenv.ReferencePendingDelete || intent.Environment != exact || intent.BindingID != string(req.SourceBindingID) {
			return nil, server.ErrPlacementChanged
		}
		op = intent.OperationID
	} else {
		var remote *executionenv.Error
		if !errors.As(err, &remote) || remote.Code != executionenv.CodeNotFound {
			return nil, mapPlacementError(err)
		}
	}
	if op == "" {
		op, err = newOperationID()
		if err != nil {
			return nil, server.ErrPlacementUnavailable
		}
	}
	mutation := executionenv.ReferenceRequest{Environment: executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}, Owner: owner, BindingID: string(req.SourceBindingID), OperationID: op}
	if err := p.client.PrepareReferenceDelete(ctx, mutation); err != nil {
		return nil, mapPlacementError(err)
	}
	return &referenceDeleteHandle{client: p.client, req: mutation}, nil
}

func (p *Provider) ReserveSuccessor(ctx context.Context, req server.PlacementSuccessorRequest) (server.PlacementBinding, error) {
	if !p.Applies(req.Ref) || req.SourceBindingID == "" || req.DestinationBindingID == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	op, err := newOperationID()
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	ref := executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}
	reservation := executionenv.ReferenceRequest{Environment: ref, Owner: owner, SourceBindingID: string(req.SourceBindingID), BindingID: string(req.DestinationBindingID), OperationID: op}
	abort := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = p.client.AbortReference(cleanupCtx, reservation)
	}
	inherited, err := p.client.ReserveSuccessor(ctx, reservation)
	if err != nil {
		abort()
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	for _, name := range req.Capabilities {
		if !slices.Contains(inherited, name) {
			abort()
			return server.PlacementBinding{}, &server.ExecutionCapabilityRefusal{
				Reason: fmt.Sprintf("the source environment was not admitted for %q", name),
			}
		}
	}
	attached, err := p.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: string(req.SourceBindingID)}, Purpose: executionenv.PurposeSession})
	if err != nil {
		abort()
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	if !sameCapabilitySet(inherited, attached.AdmittedCapabilities) {
		abort()
		return server.PlacementBinding{}, server.ErrPlacementChanged
	}
	base, err := p.binding(owner, string(req.DestinationBindingID), attached)
	if err != nil {
		abort()
		return server.PlacementBinding{}, err
	}
	return p.withExclusiveReferenceTransaction(base, owner, string(req.DestinationBindingID), op), nil
}

func (p *Provider) Applies(ref session.EnvironmentRef) bool {
	return ref.Kind == session.EnvironmentKind("kubernetes")
}

type runHandle struct {
	provider    *Provider
	owner       executionenv.Owner
	claim       executionenv.RunClaimRequest
	credentials *runContext
	environment tool.Environment
	mu          sync.Mutex
}

func (h *runHandle) Environment() tool.Environment { return h.environment }
func (h *runHandle) RenewalDeadline() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.credentials.expiry()
}
func (h *runHandle) Renew(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	op, err := newOperationID()
	if err != nil {
		return err
	}
	h.claim.OperationID = op
	claim, err := h.provider.client.RenewRun(ctx, h.claim)
	if err != nil {
		return err
	}
	h.claim.Epoch, h.claim.ClaimID = claim.Epoch, claim.ClaimID
	h.credentials.replace(claim)
	return nil
}
func (h *runHandle) Release(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	op, err := newOperationID()
	if err != nil {
		return err
	}
	h.claim.OperationID = op
	return h.provider.client.ReleaseRun(ctx, h.claim)
}

func (p *Provider) ListReferenceIntents(ctx context.Context, limit int) ([]server.ReferenceIntent, error) {
	if limit <= 0 || limit > 64 {
		limit = 64
	}
	intents, err := p.client.ListAllReferenceIntents(ctx)
	if err != nil {
		return nil, mapPlacementError(err)
	}
	if len(intents) > limit {
		intents = intents[:limit]
	}
	out := make([]server.ReferenceIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.Owner.Issuer == "" || intent.Owner.Subject == "" || intent.BindingID == "" || intent.OperationID == "" {
			return nil, server.ErrPlacementUnavailable
		}
		out = append(out, server.ReferenceIntent{
			Ref:             toSessionRef(intent.Environment),
			Principal:       &session.Principal{Issuer: intent.Owner.Issuer, Subject: intent.Owner.Subject},
			BindingID:       session.SessionID(intent.BindingID),
			SourceBindingID: session.SessionID(intent.SourceBindingID),
			OperationID:     intent.OperationID,
			PendingDelete:   intent.State == executionenv.ReferencePendingDelete,
		})
	}
	return out, nil
}

func (p *Provider) referenceIntentRequest(intent server.ReferenceIntent) (executionenv.ReferenceRequest, error) {
	owner, err := ownerOf(intent.Principal)
	if err != nil || !p.Applies(intent.Ref) || intent.BindingID == "" || intent.OperationID == "" {
		return executionenv.ReferenceRequest{}, server.ErrInvalidPlacementSelection
	}
	return executionenv.ReferenceRequest{Environment: executionenv.EnvironmentRef{ID: intent.Ref.ID, Revision: intent.Ref.Revision}, Owner: owner, BindingID: string(intent.BindingID), SourceBindingID: string(intent.SourceBindingID), OperationID: intent.OperationID}, nil
}

func (p *Provider) CommitReferenceIntent(ctx context.Context, intent server.ReferenceIntent) error {
	req, err := p.referenceIntentRequest(intent)
	if err != nil {
		return err
	}
	return mapPlacementError(p.client.CommitReference(ctx, req))
}
func (p *Provider) ConfirmReferenceIntentDelete(ctx context.Context, intent server.ReferenceIntent) error {
	req, err := p.referenceIntentRequest(intent)
	if err != nil {
		return err
	}
	return mapPlacementError(p.client.ConfirmReferenceDelete(ctx, req))
}
func (p *Provider) CancelReferenceIntentDelete(ctx context.Context, intent server.ReferenceIntent) error {
	req, err := p.referenceIntentRequest(intent)
	if err != nil {
		return err
	}
	return mapPlacementError(p.client.CancelReferenceDelete(ctx, req))
}

func (p *Provider) AcquireRun(ctx context.Context, req server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	if !p.Applies(req.Ref) || req.BindingID == "" || req.RunID == "" {
		return nil, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return nil, err
	}
	intents, err := p.client.ListReferenceIntents(ctx, owner)
	if err != nil {
		return nil, mapPlacementError(err)
	}
	for _, intent := range intents {
		if intent.Environment == (executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}) && intent.BindingID == string(req.BindingID) && intent.State == executionenv.ReferencePendingCreate {
			if err := p.client.CommitReference(ctx, executionenv.ReferenceRequest{Environment: intent.Environment, Owner: owner, BindingID: intent.BindingID, OperationID: intent.OperationID}); err != nil {
				return nil, mapPlacementError(err)
			}
		}
	}
	op, err := newOperationID()
	if err != nil {
		return nil, server.ErrPlacementUnavailable
	}
	claimReq := executionenv.RunClaimRequest{Environment: executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}, Owner: owner, BindingID: string(req.BindingID), RunID: req.RunID, OperationID: op, TTL: executionenv.DefaultRunTTL}
	claim, err := p.client.AcquireRun(ctx, claimReq)
	if err != nil {
		return nil, mapPlacementError(err)
	}
	if toSessionRef(claim.Environment) != req.Ref || claim.BindingID != string(req.BindingID) || claim.RunID != req.RunID {
		return nil, server.ErrPlacementChanged
	}
	credentials := &runContext{client: p.client, context: executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: claim.BindingID, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}, expiresAt: claim.ExpiresAt}
	ws := &runWorkspace{
		Workspace:   executorclient.NewWorkspace(p.client, remoteRoot, credentials.executorContext),
		credentials: credentials,
	}
	commands := executorclient.NewRunner(p.client, remoteRoot, credentials.executorContext)
	env, envErr := tool.NewEnvironment(req.Ref, ws, memledger.New(), commands)
	if envErr != nil {
		return nil, server.ErrPlacementUnavailable
	}
	claimReq.ClaimID, claimReq.Epoch, claimReq.GrantGeneration = claim.ClaimID, claim.Epoch, claim.GrantGeneration
	handle := &runHandle{provider: p, owner: owner, claim: claimReq, credentials: credentials, environment: env}
	credentials.renewClaim = handle.Renew
	return handle, nil
}

func (p *Provider) ListExecutionTemplates(ctx context.Context, principal *session.Principal) ([]server.ExecutionTemplateInfo, string, error) {
	if _, err := ownerOf(principal); err != nil {
		return nil, "", server.ErrPlacementNotFound
	}
	response, err := p.client.ListExecutionTemplates(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(response.GetItems()) > 64 {
		return nil, "", server.ErrInvalidPlacementBinding
	}
	items := make([]server.ExecutionTemplateInfo, 0, len(response.GetItems()))
	for _, item := range response.GetItems() {
		items = append(items, server.ExecutionTemplateInfo{
			ID:           item.GetTemplate().GetId(),
			Revision:     item.GetTemplate().GetRevision(),
			Name:         item.GetName(),
			Description:  item.GetDescription(),
			DisplayToken: item.GetDisplayToken(),
			Extensions:   item.GetExtensions(),
			Capabilities: slices.Clone(item.GetCapabilities()),
		})
	}
	return items, response.GetInventoryRevision(), nil
}

// ValidatePlacement performs side-effect-free profile and service preflight.
func (p *Provider) ValidatePlacement(ctx context.Context) error {
	if _, err := p.client.ValidateTemplate(ctx, p.profile, p.revision); err != nil {
		return mapPlacementError(err)
	}
	return p.client.CheckExecutorServices(ctx)
}

func ownerOf(principal *session.Principal) (executionenv.Owner, error) {
	if principal == nil || principal.Issuer == "" || principal.Subject == "" {
		return executionenv.Owner{}, server.ErrPlacementNotFound
	}
	return executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject}, nil
}

// Bind allocates the environment for the final session binding.
func (p *Provider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if !req.Selector.Valid() || (!req.Selector.IsDefault() && !req.Selector.IsTemplate()) || req.BindingID == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	id, revision := p.profile, p.revision
	if req.Selector.IsTemplate() {
		id, revision = req.Selector.ID, req.Selector.Revision
	}
	if revision == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	operationID := bindOperationID(owner, string(req.BindingID), id+"\x00"+revision)
	ensureDone := creatediag.Begin(ctx, "bind_ensure")
	ensured, err := p.client.EnsureTemplateCapabilities(
		ctx,
		string(req.BindingID),
		id,
		revision,
		owner,
		operationID,
		req.Capabilities,
	)
	ensureDone(err)
	var remote *executionenv.Error
	if errors.As(err, &remote) && remote.Message != genericProviderMessage {
		return server.PlacementBinding{}, &server.ExecutionCapabilityRefusal{Reason: remote.Message}
	}
	if err != nil {
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	if ensured.Environment.ID == "" || ensured.Environment.Revision == "" {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return p.waitForBinding(ctx, owner, string(req.BindingID), ensured.Environment, operationID)
}

// Reattach exactly resolves a persisted remote environment for its binding.
func (p *Provider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.BindingID == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	ref := executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}
	attached, err := p.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: string(req.BindingID)}, Purpose: executionenv.PurposeSession})
	if err != nil {
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	if toSessionRef(attached.Environment) != req.Ref {
		return server.PlacementBinding{}, server.ErrPlacementChanged
	}
	return p.binding(owner, string(req.BindingID), attached)
}

func (p *Provider) waitForBinding(ctx context.Context, owner executionenv.Owner, binding string, ref executionenv.EnvironmentRef, operationID string) (server.PlacementBinding, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	calls, last := 0, "begin"
	creatediag.Note(ctx, "attach_poll", last, calls)
	defer func() { creatediag.Note(ctx, "attach_poll_end", last, calls) }()
	for {
		calls++
		attached, err := p.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession})
		state := bindingPollState(ctx, attached.Ready, err)
		if state != last {
			last = state
			creatediag.Note(ctx, "attach_poll", state, calls)
		}
		if err == nil && attached.Ready {
			if attached.Environment != ref {
				return server.PlacementBinding{}, server.ErrPlacementChanged
			}
			base, bindErr := p.binding(owner, binding, attached)
			if bindErr != nil {
				return server.PlacementBinding{}, bindErr
			}
			return p.withSharedInitialReferenceTransaction(base, owner, binding, operationID), nil
		}
		if err != nil {
			var remote *executionenv.Error
			if !errors.As(err, &remote) || (remote.Code != executionenv.CodeNotReady && !remote.Retryable) {
				return server.PlacementBinding{}, mapPlacementError(err)
			}
		}
		select {
		case <-ctx.Done():
			last = bindingPollState(ctx, false, ctx.Err())
			return server.PlacementBinding{}, server.ErrPlacementUnavailable
		case <-ticker.C:
		}
	}
}

func bindingPollState(ctx context.Context, ready bool, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "deadline"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	var remote *executionenv.Error
	if errors.As(err, &remote) {
		if remote.Code == executionenv.CodeNotReady {
			if remote.Retryable {
				return "not_ready_retryable"
			}
			return "not_ready_nonretryable"
		}
		if remote.Retryable {
			return "remote_retryable"
		}
		return "remote_terminal"
	}
	if err != nil {
		return "transport_error"
	}
	if ready {
		return "ready"
	}
	return "pending"
}

func (p *Provider) binding(owner executionenv.Owner, binding string, attached executionenv.AttachEnvironmentResponse) (server.PlacementBinding, error) {
	if !attached.Ready || attached.Environment.ID == "" || attached.Environment.Revision == "" || attached.Epoch == 0 {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	// The engine requires a workspace, so a set without filesystem is unusable.
	if !slices.Contains(attached.AdmittedCapabilities, executionenv.CapabilityFilesystem) {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	credentials := &runContext{client: p.client, context: executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: binding, Epoch: attached.Epoch, GrantGeneration: attached.GrantGeneration}, expiresAt: time.Time{}}
	ws := &runWorkspace{
		Workspace:   executorclient.NewWorkspace(p.client, remoteRoot, credentials.executorContext),
		credentials: credentials,
	}
	var commands tool.CommandRunner
	if slices.Contains(attached.AdmittedCapabilities, executionenv.CapabilityForegroundCommand) {
		commands = executorclient.NewRunner(p.client, remoteRoot, credentials.executorContext)
	}
	sref := toSessionRef(attached.Environment)
	env, err := tool.NewEnvironment(sref, ws, memledger.New(), commands)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment: env,
		Ref:         sref,
		Metadata: server.PlacementMetadata{
			Kind:     "kubernetes",
			Label:    "Remote Kubernetes workspace",
			Revision: attached.Environment.Revision,
		},
		AdmittedCapabilities: slices.Clone(attached.AdmittedCapabilities),
	}, nil
}

// EnforcesExecutionCapabilities reports that every environment Bind allocates
// is admitted by the trusted service.
func (*Provider) EnforcesExecutionCapabilities(selector server.PlacementSelector) bool {
	return selector.IsDefault() || selector.IsTemplate()
}
func sameCapabilitySet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, name := range a {
		if !slices.Contains(b, name) {
			return false
		}
	}
	return true
}

func (p *Provider) withSharedInitialReferenceTransaction(binding server.PlacementBinding, owner executionenv.Owner, bindingID, operationID string) server.PlacementBinding {
	// Initial Bind reservations are deterministic and may be shared by another
	// replica, so local pre-publication cleanup must retain them.
	return p.withReferenceTransaction(binding, owner, bindingID, operationID, nil)
}

func (p *Provider) withExclusiveReferenceTransaction(binding server.PlacementBinding, owner executionenv.Owner, bindingID, operationID string) server.PlacementBinding {
	return p.withReferenceTransaction(binding, owner, bindingID, operationID, func(ctx context.Context, req executionenv.ReferenceRequest) error {
		return p.client.AbortReference(ctx, req)
	})
}

func (p *Provider) withReferenceTransaction(binding server.PlacementBinding, owner executionenv.Owner, bindingID, operationID string, abort func(context.Context, executionenv.ReferenceRequest) error) server.PlacementBinding {
	ref := executionenv.EnvironmentRef{ID: binding.Ref.ID, Revision: binding.Ref.Revision}
	req := executionenv.ReferenceRequest{Environment: ref, Owner: owner, BindingID: bindingID, OperationID: operationID}
	var mu sync.Mutex
	finalized := false
	binding.Commit = func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if finalized {
			return nil
		}
		// Once dispatched, a missing response is ambiguous: the provider may have
		// committed. Close must never turn that uncertainty into an abort.
		finalized = true
		commitDone := creatediag.Begin(ctx, "reference_commit")
		err := p.client.CommitReference(ctx, req)
		commitDone(err)
		return err
	}
	binding.Close = func() error {
		mu.Lock()
		defer mu.Unlock()
		if finalized {
			return nil
		}
		finalized = true
		if abort == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return abort(ctx, req)
	}
	return binding
}

func toSessionRef(ref executionenv.EnvironmentRef) session.EnvironmentRef {
	return session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: ref.ID, Revision: ref.Revision}
}
func mapPlacementError(err error) error {
	if err == nil {
		return nil
	}
	var remote *executionenv.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case executionenv.CodeNotFound, executionenv.CodePermissionDenied, executionenv.CodeUnauthenticated:
			return server.ErrPlacementNotFound
		case executionenv.CodeConflict, executionenv.CodeVersionMismatch, executionenv.CodeDirectoryNotEmpty:
			return server.ErrPlacementChanged
		case executionenv.CodeInvalidArgument:
			return server.ErrInvalidPlacementSelection
		}
	}
	return server.ErrPlacementUnavailable
}

type runContext struct {
	mu         sync.Mutex
	client     *Client
	context    executionenv.RequestContext
	expiresAt  time.Time
	renewClaim func(context.Context) error
}

func (g *runContext) current(_ context.Context) (executionenv.RequestContext, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.context.ClaimID == "" || !time.Now().Before(g.expiresAt) {
		return executionenv.RequestContext{}, &executionenv.Error{Code: executionenv.CodeUnauthenticated, Message: "run lease unavailable", Retryable: true}
	}
	return g.context, nil
}

func (g *runContext) executorContext(ctx context.Context) (*executorv1.RequestContext, error) {
	rc, err := g.current(ctx)
	if err != nil {
		return nil, err
	}
	return executorContext(rc), nil
}

func executorContext(rc executionenv.RequestContext) *executorv1.RequestContext {
	return &executorv1.RequestContext{
		Environment: &executorv1.EnvironmentRef{
			Id:       rc.Environment.ID,
			Revision: rc.Environment.Revision,
		},
		Owner:           &executorv1.Owner{Issuer: rc.Owner.Issuer, Subject: rc.Owner.Subject},
		BindingId:       rc.BindingID,
		Epoch:           rc.Epoch,
		RunId:           rc.RunID,
		ClaimId:         rc.ClaimID,
		GrantGeneration: rc.GrantGeneration,
	}
}

func (g *runContext) refresh(ctx context.Context) (executionenv.RequestContext, error) {
	g.mu.Lock()
	before := g.expiresAt
	if g.context.ClaimID == "" || !time.Now().Before(before) {
		g.mu.Unlock()
		return executionenv.RequestContext{}, &executionenv.Error{Code: executionenv.CodeUnauthenticated, Message: "active run lease expired; retry the run", Retryable: true}
	}
	renew := g.renewClaim
	g.mu.Unlock()
	g.mu.Lock()
	if g.expiresAt.After(before) && time.Now().Before(g.expiresAt) {
		out := g.context
		g.mu.Unlock()
		return out, nil
	}
	g.mu.Unlock()
	if renew == nil {
		return executionenv.RequestContext{}, &executionenv.Error{Code: executionenv.CodeUnauthenticated, Message: "active run lease cannot be renewed", Retryable: true}
	}
	if err := renew(ctx); err != nil {
		return executionenv.RequestContext{}, err
	}
	return g.current(ctx)
}

func (g *runContext) expiry() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.expiresAt
}

func (g *runContext) replace(claim executionenv.RunClaim) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.context.RunID, g.context.ClaimID, g.context.Epoch, g.context.GrantGeneration = claim.RunID, claim.ClaimID, claim.Epoch, claim.GrantGeneration
	g.expiresAt = claim.ExpiresAt
}
