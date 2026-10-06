//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestKindExecutionProductionScopedAdministrator(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	qualifyDistinctAdministrator(t, ctx, state, kubeconfig, "initial")
}

func qualifyDistinctAdministrator(t *testing.T, ctx context.Context, state, kubeconfig, phase string) {
	t.Helper()
	creator, forward := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, creator, "admin-"+phase)
	clients := map[string]*executionclient.Client{}
	for _, name := range []string{"operations", "wrong-scope", "intruder"} {
		client, err := executionclient.New(forward.addr, loadTLS(t, filepath.Join(state, "pki"), name, "mecatl-execution.execution-qualification.svc.cluster.local"))
		if err != nil {
			t.Fatal("create synthetic admin client")
		}
		t.Cleanup(func() { client.Close() })
		clients[name] = client
	}
	rc, release := acquireRun(t, ctx, creator, owner, binding, attached, "admin-data-"+phase)
	if _, err := creator.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "admin-sentinel", Data: []byte("creator-only\n")}); err != nil {
		t.Fatal("creator data-plane positive:", remoteErrorCode(err))
	}
	admin := clients["operations"]
	if _, err := admin.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession}); !isRemoteCode(err, executionenv.CodeNotFound) {
		t.Fatal("scoped administrator acquired creator attach privilege:", remoteErrorCode(err))
	}
	if _, err := admin.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "admin-sentinel"}); !isRemoteCode(err, executionenv.CodePermissionDenied) {
		t.Fatal("scoped administrator used creator file grant:", remoteErrorCode(err))
	}
	if _, err := admin.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "true"}); !isRemoteCode(err, executionenv.CodePermissionDenied) {
		t.Fatal("scoped administrator used creator command grant:", remoteErrorCode(err))
	}
	release()
	before := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	request := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: before.Epoch, ExpectedPodUID: before.PodUID, ExpectedPVCUID: before.PVCUID, OperationID: "admin-replace-" + phase}
	for name, code := range map[string]executionenv.ErrorCode{"wrong-scope": executionenv.CodeNotFound, "intruder": executionenv.CodePermissionDenied} {
		if err := clients[name].ReplaceExecutor(ctx, request); !isRemoteCode(err, code) {
			t.Fatalf("%s admin refusal: %s", name, remoteErrorCode(err))
		}
	}
	if after := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID); after != before {
		t.Fatal("denied admin calls changed runtime state")
	}
	if err := admin.ReplaceExecutor(ctx, request); err != nil {
		t.Fatal("distinct scoped administrator replacement:", remoteErrorCode(err))
	}
	after := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Ready && s.PodUID != before.PodUID })
	if after.PVCUID != before.PVCUID || after.Epoch <= before.Epoch {
		t.Fatal("scoped replacement failed exact PVC/epoch reconciliation")
	}
	reattached := waitReady(t, ctx, creator, owner, binding, attached.Environment)
	rc, release = acquireRun(t, ctx, creator, owner, binding, reattached, "admin-verify-"+phase)
	waitFileContent(t, ctx, creator, rc, "admin-sentinel", "creator-only\n", "scoped replacement")
	release()
	retireSyntheticEnvironment(t, ctx, creator, kubeconfig, owner, binding, attached.Environment)
}
