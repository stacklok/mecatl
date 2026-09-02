package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func TestCloudNativeLearning_Scenario3_ExplicitProcedureAttemptSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	userModelDir := t.TempDir()
	owner := &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser}
	ownerCtx := session.WithPrincipal(ctx, owner)

	lifecycle, stopFirstProcess := context.WithCancel(ctx)
	firstProvider := mockllm.New(mockllm.TextTurn("workflow completed"))
	first, err := Build(lifecycle, learningRestartConfig(workspace, storeDir, userModelDir, firstProvider))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := first.Service.CreateSession(ownerCtx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	// Stop only Build-owned workers. The ordinary run remains live and must still
	// durably admit its explicit procedure request before the process is replaced.
	stopFirstProcess()
	run, err := first.Service.StartRun(ownerCtx, sess.ID, "Turn this workflow into a skill")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	events := drainLearningRun(run)
	log, err := jsonlstore.New(storeDir)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	for _, event := range events {
		if err := log.Append(ctx, sess.ID, event); err != nil {
			first.Close()
			t.Fatal(err)
		}
	}
	first.Close()

	attempts, err := attemptstore.New(filepath.Join(userModelDir, "learning-attempts"))
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	page, err := attempts.List(ctx, partition, learning.AttemptList{})
	if err != nil || len(page.Records) != 1 || page.Records[0].State != learning.AttemptQueued {
		t.Fatalf("first-process durable attempts = %+v, err=%v; want one queued attempt", page.Records, err)
	}
	attemptID := page.Records[0].ID

	secondProvider := mockllm.New(mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"procedure","name":"restart-safe-workflow","title":"Restart-safe workflow","body":"Follow the verified workflow exactly.","evidence":["m:0"]}]}`))
	secondConfig := learningRestartConfig(workspace, storeDir, userModelDir, secondProvider)
	diagnostics := &recordingDiag{}
	secondConfig.Diagnostics = diagnostics
	second, err := Build(ctx, secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	deadline := time.Now().Add(5 * time.Second)
	var terminal learning.AttemptRecord
	for time.Now().Before(deadline) {
		terminal, _, err = attempts.Get(ctx, partition, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal.State.Terminal() {
			break
		}
		runtime.Gosched()
	}
	if terminal.State != learning.AttemptCompleted || terminal.Outcome != learning.AttemptOutcomeSucceeded || terminal.ProposalID == "" {
		recoveryErr, _ := diagnostics.attr("error")
		t.Fatalf("restarted attempt = %+v, want completed success linked to proposal; recovery error=%v diagnostics=%v", terminal, recoveryErr, diagnostics.messages())
	}
	proposals, err := second.Service.ListLearningProposals(ownerCtx, "", "", 10, workspace)
	if err != nil || len(proposals.GetProposals()) != 1 || proposals.GetProposals()[0].GetId() != string(terminal.ProposalID) {
		t.Fatalf("authorized linked proposals = %+v, err=%v", proposals.GetProposals(), err)
	}
	if secondProvider.Calls() != 1 {
		t.Fatalf("restart worker provider calls = %d, want exact reconstructed reflection once", secondProvider.Calls())
	}
}

func TestADR_0259_WorkerSourceAuthorityFailsClosedWhenSourceDeletedAcrossBuild(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	userModelDir := t.TempDir()
	owner := &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser}
	ownerCtx := session.WithPrincipal(ctx, owner)

	lifecycle, stopFirstProcess := context.WithCancel(ctx)
	first, err := Build(lifecycle, learningRestartConfig(workspace, storeDir, userModelDir, mockllm.New(mockllm.TextTurn("workflow completed"))))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := first.Service.CreateSession(ownerCtx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	stopFirstProcess()
	run, err := first.Service.StartRun(ownerCtx, sess.ID, "Turn this workflow into a skill")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	_ = drainLearningRun(run)
	first.Close()

	attempts, err := attemptstore.New(filepath.Join(userModelDir, "learning-attempts"))
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	page, err := attempts.List(ctx, partition, learning.AttemptList{})
	if err != nil || len(page.Records) != 1 || page.Records[0].State != learning.AttemptQueued {
		t.Fatalf("queued attempt before source deletion = %+v, err=%v", page.Records, err)
	}

	sources, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Delete(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}

	second, err := Build(ctx, learningRestartConfig(workspace, storeDir, userModelDir, mockllm.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	deadline := time.Now().Add(5 * time.Second)
	var terminal learning.AttemptRecord
	for time.Now().Before(deadline) {
		terminal, _, err = attempts.Get(ctx, partition, page.Records[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal.State.Terminal() {
			break
		}
		runtime.Gosched()
	}
	if terminal.State != learning.AttemptFailed || terminal.FailureCode != learning.FailureEvidenceUnavailable || terminal.ProposalID != "" || terminal.SkillID != "" {
		t.Fatalf("deleted-source recovery terminal = %+v, want safe evidence_unavailable without mutation", terminal)
	}
}

func TestCloudNativeLearning_Scenario3_UnwiredLearningIsByteIdentical(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	userModelDir := t.TempDir()
	baselineUserModelDir := t.TempDir()
	var baselineRequest port.LLMRequest
	baselineProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { baselineRequest = request })}, mockllm.TextTurn("ordinary result"))
	baseline, err := Build(ctx, Config{Model: "mock", Workspace: workspace, NoSoul: true, LearningMode: learning.Off, UserModelDir: baselineUserModelDir, MockProvider: baselineProvider})
	if err != nil {
		t.Fatal(err)
	}
	baselineSession, err := baseline.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		baseline.Close()
		t.Fatal(err)
	}
	baselineRun, err := baseline.Service.StartRun(ctx, baselineSession.ID, "ordinary prompt")
	if err != nil {
		baseline.Close()
		t.Fatal(err)
	}
	baselineText := drainRun(baselineRun)
	baseline.Close()

	var offRequest port.LLMRequest
	offProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		if offRequest.Model == "" {
			offRequest = request
		}
	})},
		mockllm.TextTurn("ordinary result"),
		mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`),
	)
	off, err := Build(ctx, Config{Model: "mock", Workspace: workspace, NoSoul: true, LearningMode: learning.Off, UserModelDir: userModelDir, MockProvider: offProvider})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	offSession, err := off.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	offRun, err := off.Service.StartRun(ctx, offSession.ID, "ordinary prompt")
	if err != nil {
		t.Fatal(err)
	}
	offText := drainRun(offRun)
	if baselineText != offText || !reflect.DeepEqual(baselineRequest, offRequest) {
		t.Fatalf("off ordinary run differs from unwired baseline:\nbaseline text=%q request=%+v\noff text=%q request=%+v", baselineText, baselineRequest, offText, offRequest)
	}
	if _, err = off.Service.ReflectSession(ctx, offSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(userModelDir, "learning-attempts")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("off learning allocated durable attempt repository: %v", statErr)
	}

	// Off suppresses automatic admission, not an explicitly configured repository
	// connection. The latter remains available for explicit reflection, learned-skill
	// inspection, and recovery of attempts admitted by another process.
	remoteAddr := startSourceDriver(t, func(server *grpc.Server) {
		driverv1.RegisterLearningRepositoryCapabilitiesServiceServer(server, grpcdriver.NewLearningRepositoryCapabilitiesServer(grpcdriver.LearningRepositoryCapabilities{
			AttemptRepository: true, ProposalRepository: true, SkillRepository: true,
			OwnershipMode: grpcdriver.LearningRepositoryOwnershipTrusted,
		}))
	})
	attempts, proposals, skills, ledger, closeRemote, err := resolveLearningRepositories(ctx, Config{
		LearningStoreURL: remoteAddr,
		LearningMode:     learning.Off,
		driverConns:      driverConnsForTest(t),
	})
	if closeRemote != nil {
		defer closeRemote()
	}
	if err != nil {
		t.Fatalf("off configured learning repository: %v", err)
	}
	if attempts == nil || proposals == nil || skills == nil || ledger != nil {
		t.Fatalf("off configured repositories = (%T, %T, %T), ledger=%T; want inspected repository set without automatic ledger", attempts, proposals, skills, ledger)
	}
}

func drainLearningRun(run interface {
	Events() <-chan session.Event
	Approve(string, session.ApprovalVerdict)
}) []session.Event {
	var events []session.Event
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
		events = append(events, event)
	}
	return events
}

func learningRestartConfig(workspace, storeDir, userModelDir string, provider port.LLMProvider) Config {
	return Config{
		Model: "mock", Workspace: workspace, StoreDir: storeDir, UserModelDir: userModelDir,
		NoSoul: true, LearningMode: learning.Review, MockProvider: provider,
	}
}
