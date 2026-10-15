package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/adapters/grpcdriver"
	"github.com/stacklok/mecatl/adapters/jsonlstore"
	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Missing event evidence is intentional: placement reattachment precedes evidence
// validation and does not require a model call.
func TestBuildAttemptRecoveryWaitsForComposition(t *testing.T) {
	synctest.Test(t, testBuildAttemptRecoveryWaitsForComposition)
}

func testBuildAttemptRecoveryWaitsForComposition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace := osfsWSForTest(t, t.TempDir()).Root()
	storeDir, userModelDir := t.TempDir(), t.TempDir()
	clock := &attemptWorkerClock{now: time.Now()}
	base := memattempt.New(wallclock.Clock{})
	partition, record := createRecoveryAttempt(t, base, clock)
	sources, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	source := session.New(record.Provenance.Source.SessionID, session.ModeDefault, configuredLocalPlacementRef(workspace), defaultLimits(), time.Now())
	source.ProviderID = providerOpenRouter
	if err := sources.Save(ctx, source); err != nil {
		t.Fatal(err)
	}

	if loaded, err := sources.Load(ctx, record.Provenance.Source.SessionID); err != nil || loaded == nil {
		t.Fatalf("load seeded source: %v", err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	claimed, listing, finalized := make(chan struct{}), make(chan struct{}), make(chan struct{})
	repository := &startupGatedAttemptRepository{AttemptRepository: base, claimed: claimed, release: release, finalized: finalized}
	ledger := automaticStoreForTest(t, t.TempDir(), defaultLearningAutomaticConfig())
	remoteAddr, conns := startStartupLoopDriver(t, nil, func(s *grpc.Server) {
		driverv1.RegisterLearningRepositoryCapabilitiesServiceServer(s, grpcdriver.NewLearningRepositoryCapabilitiesServer(grpcdriver.LearningRepositoryCapabilities{
			AttemptRepository: true, ProposalRepository: true, SkillRepository: true,
			ValidatedSkillActivation: true, AutomaticAdmissionLedger: true,
			OwnershipMode: grpcdriver.LearningRepositoryOwnershipTrusted,
		}))
		driverv1.RegisterAttemptRepositoryServiceServer(s, grpcdriver.NewAttemptRepositoryServer(repository))
		driverv1.RegisterProposalRepositoryServiceServer(s, grpcdriver.NewProposalRepositoryServer(memproposal.New()))
		driverv1.RegisterSkillRepositoryServiceServer(s, grpcdriver.NewSkillRepositoryServer(memskill.New()))
		driverv1.RegisterAutomaticAdmissionLedgerServiceServer(s, grpcdriver.NewAutomaticAdmissionLedgerServer(ledger))
	})
	var listingOnce sync.Once
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		listingOnce.Do(func() { close(listing) })
		select {
		case <-release:
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("offline fixture"))}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	cfg := isolateConfig(t, Config{
		Workspace: workspace, StoreDir: storeDir, UserModelDir: userModelDir,
		NoSoul: true, NoShell: true, TrustProject: true,
		LearningMode: learning.Auto, LearningAutomatic: defaultLearningAutomaticConfig(), LearningStoreURL: remoteAddr,
		envDetector:         fakeEnv(map[string]string{"OPENROUTER_API_KEY": "offline"}),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return mockllm.New() },
		liveModelHTTPClient: client, liveModelRefreshSync: true, driverConns: conns,
	})
	type result struct {
		built *Built
		err   error
	}
	done := make(chan result, 1)
	go func() { built, err := Build(ctx, cfg); done <- result{built, err} }()
	defer func() {
		unblock()
		cancel()
		got := <-done
		if got.built != nil {
			got.built.Close()
		}
		if got.err != nil {
			t.Errorf("Build: %v", got.err)
		}
	}()
	select {
	case <-listing:
	case <-time.After(5 * time.Second):
		t.Fatal("late startup listing did not enter")
	}
	synctest.Wait()
	select {
	case <-claimed:
		t.Fatal("recovery claimed work before composition finished")
	default:
	}
	select {
	case got := <-done:
		done <- got
		t.Fatalf("Build returned before gated model discovery: %v", got.err)
	default:
	}
	unblock()
	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not claim work after composition finished")
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after gate release")
	}
	persisted, found, err := base.Get(ctx, partition, record.ID)
	if err != nil || !found || persisted.State != learning.AttemptFailed || persisted.FailureCode != learning.FailureEvidenceUnavailable {
		t.Fatalf("recovery record = %+v, found=%v, err=%v; want evidence_unavailable after local reattachment", persisted, found, err)
	}
}

type startupGatedAttemptRepository struct {
	learning.AttemptRepository
	claimed   chan struct{}
	release   <-chan struct{}
	finalized chan struct{}
}

func (r *startupGatedAttemptRepository) AcquireClaim(ctx context.Context, p learning.AttemptPartition, id learning.AttemptID, v learning.AttemptVersion, ttl time.Duration) (learning.AttemptRecord, learning.AttemptClaim, error) {
	record, claim, err := r.AttemptRepository.AcquireClaim(ctx, p, id, v, ttl)
	if err != nil {
		return record, claim, err
	}
	close(r.claimed)
	select {
	case <-r.release:
		return record, claim, nil
	case <-ctx.Done():
		return record, claim, ctx.Err()
	}
}

func (r *startupGatedAttemptRepository) Finalize(ctx context.Context, p learning.AttemptPartition, id learning.AttemptID, v learning.AttemptVersion, claim learning.AttemptClaim, f learning.AttemptFinalization) (learning.AttemptRecord, error) {
	record, err := r.AttemptRepository.Finalize(ctx, p, id, v, claim, f)
	if err == nil {
		close(r.finalized)
	}
	return record, err
}
