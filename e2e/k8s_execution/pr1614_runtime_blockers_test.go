//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestKindExecutionRevocationStopsActiveWriterAndRetainsFenceUncertainty(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "revoke-active-writer")
	rc, _ := acquireRun(t, ctx, client, owner, binding, attached, "revoke-active-writer")
	initial := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)

	done := make(chan error, 1)
	go func() {
		_, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: `i=0; while [ $i -lt 120 ]; do i=$((i+1)); printf '%s\n' "$i" > revoke-counter; sleep 1; done`, TimeoutMillis: 150000})
		done <- err
	}()
	waitActiveOperationOrCommandError(t, ctx, kubeconfig, attached.Environment.ID, done)
	if _, err := client.RevokeEnvironment(ctx, attached.Environment, owner, rc.GrantGeneration, "revoke-active-writer"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked active writer returned clean completion")
		}
	case <-time.After(25 * time.Second):
		t.Fatal("revoked active writer did not stop promptly")
	}
	fenced := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool {
		return s.FenceState == "FenceUnknown" && s.ActiveOperationID != ""
	})
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "post-revoke-writer", Data: []byte("forbidden")}); err == nil {
		t.Fatal("revoked grant admitted a new writer")
	}
	pod := kubeValue(t, ctx, kubeconfig, "get", "pod", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[0].metadata.name}")
	first := strings.TrimSpace(string(runKubectl(t, ctx, kubeconfig, "exec", "-n", namespace, pod, "--", "cat", "/workspace/revoke-counter")))
	time.Sleep(2 * time.Second)
	second := strings.TrimSpace(string(runKubectl(t, ctx, kubeconfig, "exec", "-n", namespace, pod, "--", "cat", "/workspace/revoke-counter")))
	if first == "" || first != second {
		t.Fatalf("workspace kept changing after RPC stopped: first=%q second=%q", first, second)
	}

	contextName := os.Getenv("MECATL_KUBE_CONTEXT")
	if contextName == "" {
		t.Fatal("explicit fixture Kubernetes context required")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: contextName}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 15 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	envUID := kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	if err := terminateOwnedExecutor(ctx, kube, attached.Environment.ID, envUID, initial.PodUID); err != nil {
		t.Fatal(err)
	}
	waitExecutorTerminal(ctx, t, kubeconfig, attached.Environment.ID, initial.PodUID)
	if err := client.RecoverEnvironment(ctx, executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: initial.Epoch, ExpectedPodUID: initial.PodUID, ExpectedPVCUID: fenced.PVCUID, OperationID: "recover-revoke-active-writer"}); err != nil {
		t.Fatal(err)
	}
	retireSyntheticEnvironment(t, ctx, client, kubeconfig, owner, binding, attached.Environment)
}

func TestKindExecutionExpiredRunAllowsFinalReferenceRetireAndDelete(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "expired-final-reference")
	claim, err := client.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: "expired-final-reference", OperationID: "acquire-expired-final-reference", TTL: executionenv.MinRunTTL})
	if err != nil {
		t.Fatal(err)
	}
	rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: binding, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Grant: claim.Grant}
	status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	commandDone := make(chan error, 1)
	go func() {
		_, commandErr := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "sleep 3", TimeoutMillis: 10000})
		commandDone <- commandErr
	}()
	waitActiveOperationOrCommandError(t, ctx, kubeconfig, attached.Environment.ID, commandDone)
	deleteRef := executionenv.ReferenceRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, OperationID: "delete-expired-final-reference"}
	if err := client.PrepareReferenceDelete(ctx, deleteRef); err != nil {
		t.Fatal(err)
	}
	if err := client.ConfirmReferenceDelete(ctx, deleteRef); err != nil {
		t.Fatal(err)
	}
	retire := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: status.Epoch, ExpectedPodUID: status.PodUID, ExpectedPVCUID: status.PVCUID, OperationID: "retire-expired-final-reference"}
	if err := client.RetireEnvironment(ctx, retire); err == nil {
		t.Fatal("retirement admitted active work")
	}
	if err := <-commandDone; err != nil {
		t.Fatal(err)
	}
	if err := client.RetireEnvironment(ctx, retire); err == nil {
		t.Fatal("retirement admitted an unexpired run")
	}
	wait := time.Until(claim.ExpiresAt) + time.Second
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		}
	}
	if err := client.RetireEnvironment(ctx, retire); err != nil {
		t.Fatalf("expired exact run blocked retirement: %v", err)
	}
	retired := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Retired && s.ExecutorTerminated })
	if err := client.DeleteRetiredEnvironment(ctx, attached.Environment, owner, retired.PVCUID, "delete-retained-expired-final-reference"); err != nil {
		t.Fatal(err)
	}
	waitResourceAbsent(t, ctx, kubeconfig, "executionenvironment", attached.Environment.ID)
}

type kindInitialCreateBarrierStore struct {
	*memstore.Store
	target  session.SessionID
	entered int
	mu      sync.Mutex
	release chan struct{}
}

func (s *kindInitialCreateBarrierStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if id == s.target {
		s.mu.Lock()
		if s.entered < 2 {
			s.entered++
			if s.entered == 2 {
				close(s.release)
			}
			gate := s.release
			s.mu.Unlock()
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			s.mu.Unlock()
		}
	}
	return s.Store.Load(ctx, id)
}

func TestKindExecutionServiceExplicitIDRetriesReuseOneAllocation(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	provider, err := executionclient.NewProvider(client, "go")
	if err != nil {
		t.Fatal(err)
	}
	principal := &session.Principal{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: fmt.Sprintf("service-explicit-%d", time.Now().UnixNano()), GrantType: session.GrantTypeUser}
	principalCtx := session.WithPrincipal(ctx, principal)
	id := session.SessionID(fmt.Sprintf("sched--kind-explicit-%d", time.Now().UnixNano()))
	store := &kindInitialCreateBarrierStore{Store: memstore.New(), target: id, release: make(chan struct{})}
	newService := func() *server.Service {
		engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
		svc, newErr := server.NewService(server.Config{Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "kind-explicit-id", OwnershipEnforced: true, SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine}, nil
		}})
		if newErr != nil {
			t.Fatal(newErr)
		}
		return svc
	}
	type createResult struct {
		created *session.Session
		err     error
	}
	createConcurrently := func(services []*server.Service) []*session.Session {
		t.Helper()
		out := make(chan createResult, len(services))
		for _, svc := range services {
			go func() {
				created, createErr := svc.CreateSessionWithProfile(principalCtx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
				out <- createResult{created: created, err: createErr}
			}()
		}
		results := make([]*session.Session, 0, len(services))
		for range services {
			select {
			case result := <-out:
				if result.err != nil || result.created == nil {
					t.Fatalf("concurrent create=(%+v,%v), want success", result.created, result.err)
				}
				results = append(results, result.created)
			case <-ctx.Done():
				t.Fatalf("concurrent create did not finish: %v", ctx.Err())
			}
		}
		return results
	}
	initialServices := []*server.Service{newService(), newService()}
	initial := createConcurrently(initialServices)
	for _, svc := range initialServices {
		svc.Close()
	}
	first := initial[0]
	if initial[1].EnvironmentRef != first.EnvironmentRef {
		t.Fatalf("initial creates diverged: first=%+v second=%+v", first.EnvironmentRef, initial[1].EnvironmentRef)
	}
	services := []*server.Service{newService(), newService()}
	defer services[0].Close()
	defer services[1].Close()
	results := createConcurrently(services)
	for i := range results {
		if results[i].EnvironmentRef != first.EnvironmentRef {
			t.Fatalf("retry %d=%+v, want ref %+v", i, results[i].EnvironmentRef, first.EnvironmentRef)
		}
	}
	other := session.WithPrincipal(ctx, &session.Principal{Issuer: principal.Issuer, Subject: principal.Subject + "-other", GrantType: session.GrantTypeUser})
	if _, err := services[0].CreateSessionWithProfile(other, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); err == nil {
		t.Fatal("changed owner adopted explicit-ID allocation")
	}
	if _, err := services[0].CreateSessionWithProfile(principalCtx, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed mode error=%v", err)
	}
	if _, err := services[0].CreateSessionWithProfile(principalCtx, session.ModeDefault, session.Limits{MaxTurns: 1}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed limits error=%v", err)
	}
	if _, err := services[0].CreateSessionWithProfile(principalCtx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed profile error=%v", err)
	}
	owner := executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject}
	retireSyntheticEnvironment(t, ctx, client, kubeconfig, owner, string(id), executionenv.EnvironmentRef{ID: first.EnvironmentRef.ID, Revision: first.EnvironmentRef.Revision})
}
