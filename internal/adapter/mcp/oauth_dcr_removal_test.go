package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type removalStore struct{ credentialstore.Store }

func (removalStore) Capabilities() credentialstore.Capabilities {
	return credentialstore.Capabilities{Persistent: true, CrossProcessCAS: true}
}

func removalFixture(t *testing.T, state string) (OAuthOptions, credentialstore.Store, []byte) {
	t.Helper()
	store := newDCRMemoryStore(t)
	resource := "https://connector.example/mcp"
	identity := oauthDCRIdentity{Profile: "connector", Principal: "local-user", Resource: resource, Issuer: "https://issuer.example"}
	generation := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	path := oauthDCRCallbackPrefix + generation
	meta := oauthDCRMetadata{Issuer: identity.Issuer, Resource: resource, RedirectPolicy: oauthDCRRedirectPolicy, RedirectPath: path, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scopes: []string{oauthDCRScope}}
	record := oauthDCRRecord{Schema: oauthDCRRegistrationSchema, Version: oauthDCRRegistrationVersion, Identity: identity, Generation: generation, State: state, AttemptStartedAt: "2026-09-10T10:00:00Z", Metadata: meta, MetadataFingerprint: fingerprintDCRMetadata(meta), Registration: &oauthDCRRegistration{ClientID: "client", RegisteredRedirectURI: "http://127.0.0.1:49152" + path}}
	if state == oauthDCRStatePending {
		record.Registration = nil
	}
	key, err := oauthDCRLifecycleKey("connector")
	if err != nil {
		t.Fatal(err)
	}
	value, err := encodeOAuthDCRRecord(record, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), key, value, nil); err != nil {
		t.Fatal(err)
	}
	return OAuthOptions{Subject: OAuthSubject{Profile: identity.Profile, Principal: identity.Principal}, Issuer: identity.Issuer, Client: OAuthClientConfig{DCR: &OAuthDCRConfig{ServerName: "connector"}}, CredentialStore: removalStore{store}}, store, key
}

func TestRemoveOAuthDCRPublishesTombstoneAndIsIdempotent(t *testing.T) {
	opts, store, key := removalFixture(t, oauthDCRStateReady)
	result, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", opts)
	if err != nil || !result.LifecycleFound {
		t.Fatalf("remove = %#v, %v", result, err)
	}
	stored, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeOAuthDCRRecordRaw(stored.Value)
	if err != nil || record.State != oauthDCRStateRemoved {
		t.Fatalf("tombstone = %#v, %v", record, err)
	}
	again, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", opts)
	if err != nil || !again.LifecycleFound {
		t.Fatalf("idempotent remove = %#v, %v", again, err)
	}
}

func TestRemoveOAuthDCRResumesRemovingAfterCrash(t *testing.T) {
	opts, store, key := removalFixture(t, oauthDCRStateRemoving)
	result, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", opts)
	if err != nil || !result.LifecycleFound {
		t.Fatalf("resume remove = %#v, %v", result, err)
	}
	stored, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeOAuthDCRRecordRaw(stored.Value)
	if err != nil || record.State != oauthDCRStateRemoved {
		t.Fatalf("resumed tombstone = %#v, %v", record, err)
	}
}

func TestRemoveOAuthDCRBlocksPendingAndAllowsMissing(t *testing.T) {
	opts, _, _ := removalFixture(t, oauthDCRStatePending)
	if _, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", opts); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("pending remove = %v, want recovery required", err)
	}
	store := newDCRMemoryStore(t)
	missing := opts
	missing.CredentialStore = removalStore{store}
	// A lifecycle-not-found result is the only outcome callers may pair with
	// settings-only deletion.
	result, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", missing)
	if err != nil || result.LifecycleFound {
		t.Fatalf("missing remove = %#v, %v", result, err)
	}
}

func TestRemoveOAuthDCRBlocksReadOnlyStore(t *testing.T) {
	opts, store, _ := removalFixture(t, oauthDCRStateReady)
	opts.CredentialStore = store
	if _, err := RemoveOAuthDCR(context.Background(), "https://connector.example/mcp", opts); !errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		t.Fatalf("read-only remove = %v, want recovery required", err)
	}
}

// TestPrepareOAuthDCRLoginAfterRemovalRegistersFreshClient pins AC6.4: a valid
// removed tombstone must permit a later "add" to start a fresh pending
// generation and perform a genuinely new upstream registration — it must NOT
// be classified as corrupt/recovery-required the way every other non-ready,
// non-pending state is.
func TestPrepareOAuthDCRLoginAfterRemovalRegistersFreshClient(t *testing.T) {
	fixture := newDCRMetadataFixture(t)
	resource, store := fixture.server.URL+"/gw/mcp", newDCRMemoryStore(t)
	opts := fixture.options(t, store)
	opts.CredentialStore = removalStore{store}
	opts.Presenter = OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(raw)
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})

	bootstrap := func() oauthDCRRecord {
		prepared, path, err := PrepareOAuthDCRLogin(context.Background(), resource, opts, OAuthDCRLoginReuse)
		if err != nil {
			t.Fatalf("PrepareOAuthDCRLogin: %v (category=%v)", err, OAuthDCRRecoveryCategoryOf(err))
		}
		prepared.RedirectURL = "http://127.0.0.1:49152" + path
		controller, err := NewOAuthController(context.Background(), resource, prepared)
		if err != nil {
			t.Fatalf("NewOAuthController: %v", err)
		}
		req, resp := dcrChallenge(t, resource)
		if err := controller.Authorize(context.Background(), req, resp); err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		key, err := oauthDCRLifecycleKey(opts.Client.DCR.ServerName)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := decodeOAuthDCRRecordRaw(record.Value)
		if err != nil {
			t.Fatal(err)
		}
		if ready.State != oauthDCRStateReady {
			t.Fatalf("bootstrap did not reach ready: %#v", ready)
		}
		return ready
	}

	firstReady := bootstrap()
	if fixture.registerCount != 1 {
		t.Fatalf("registerCount after first bootstrap = %d, want 1", fixture.registerCount)
	}

	if result, err := RemoveOAuthDCR(context.Background(), resource, opts); err != nil || !result.LifecycleFound {
		t.Fatalf("remove = %#v, %v", result, err)
	}

	// The bug this pins: before the fix, reusing a removed profile was
	// classified OAuthDCRRecoveryCorrupt instead of starting a fresh pending
	// generation. bootstrap() itself calls PrepareOAuthDCRLogin(Reuse) and
	// fails the test if it errors, then drives the fresh registration all
	// the way to ready.
	secondReady := bootstrap()
	if fixture.registerCount != 2 {
		t.Fatalf("registerCount after re-add = %d, want 2 (a genuinely new upstream registration)", fixture.registerCount)
	}
	if secondReady.Generation == firstReady.Generation {
		t.Fatal("re-add after removal reused the old generation instead of registering fresh")
	}
	if secondReady.Registration.ClientID == "" || secondReady.Registration.ClientID != fixture.clientID {
		t.Fatalf("re-add client id = %q, want the fixture's registered client %q", secondReady.Registration.ClientID, fixture.clientID)
	}
}

