package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type countedFileBackend struct {
	*fakeBackend
	calls int
}

func (b *countedFileBackend) File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error) {
	b.calls++
	return executionenv.FileResponse{}, nil
}

func TestEveryFileOperationRequiresExactCurrentGrant(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	const client = "spiffe://cluster/ns/mecak8s"
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	for value, name := range executionv1.FileOperation_name {
		op, ok := operationFromProto(executionv1.FileOperation(value))
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, failure := range []string{"client", "owner", "environment", "revision", "epoch", "operation", "expired", "revoked"} {
				t.Run(failure, func(t *testing.T) {
					backend := &countedFileBackend{fakeBackend: newFakeBackend()}
					verifier := executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"key": public}, Issuer: "provider", Audience: "executor", MaxLifetime: time.Minute, Now: func() time.Time { return now }}
					h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{client: {MayAttestOwner: true}}, Verifier: verifier}, backend)
					claims := executionenv.GrantClaims{KeyID: "key", Issuer: "provider", Audience: "executor", Client: client, OwnerHash: ownerHash(owner), BindingID: "binding", RunID: "run", ClaimID: "claim", Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, Epoch: 1, GrantGeneration: 1, Operations: []executionenv.Operation{op}, NotBefore: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Second), Nonce: "nonce"}
					sign := func() string {
						token, err := executionenv.SignGrant(private, claims)
						if err != nil {
							t.Fatal(err)
						}
						return token
					}
					req := &executionv1.FileRequest{Context: &executionv1.RequestContext{Environment: refToProto(claims.Environment), Owner: &executionv1.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, BindingId: "binding", RunId: "run", ClaimId: "claim", Epoch: 1, GrantGeneration: 1, Grant: sign()}, Operation: executionv1.FileOperation(value), Path: "file"}
					switch op {
					case executionenv.OpFileReplace:
						req.Version = []byte("version")
					case executionenv.OpFileCopy, executionenv.OpFileRename:
						req.Destination = "destination"
					case executionenv.OpFileGlob:
						req.Path = ""
						req.Pattern = "*"
					case executionenv.OpFileGrep:
						req.Pattern = "text"
					}
					ctx := authenticatedContext(client)
					if _, err := h.Files(ctx, req); err != nil || backend.calls != 1 {
						t.Fatalf("positive control: calls=%d err=%v", backend.calls, err)
					}
					negative := proto.Clone(req).(*executionv1.FileRequest)
					want := codes.PermissionDenied
					switch failure {
					case "client":
						claims.Client = "spiffe://cluster/ns/other"
					case "owner":
						claims.OwnerHash = "other"
					case "environment":
						claims.Environment.ID = "other"
					case "revision":
						claims.Environment.Revision = "other"
					case "epoch":
						claims.Epoch++
					case "operation":
						claims.Operations = []executionenv.Operation{executionenv.OpCommandStart}
					case "expired":
						claims.NotBefore = now.Add(-time.Minute)
						claims.ExpiresAt = now.Add(-time.Second)
						want = codes.Unauthenticated
					case "revoked":
						h.cfg.Verifier.RevokedNonces = map[string]struct{}{"nonce": {}}
					}
					negative.Context.Grant = sign()
					if _, err := h.Files(ctx, negative); status.Code(err) != want || backend.calls != 1 {
						t.Fatalf("negative control: calls=%d code=%v err=%v", backend.calls, status.Code(err), err)
					}
				})
			}
		})
	}
}
