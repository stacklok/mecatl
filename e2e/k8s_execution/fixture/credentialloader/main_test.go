//go:build kind_execution_e2e

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestLoadCredentialAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")
	sentinel := "synthetic-provider-secret-never-print"
	if err := os.WriteFile(path, []byte("  "+sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatal("credential was not trimmed")
	}
	clear(got)
	if out := classify(errors.New("request rejected: " + sentinel)); strings.Contains(out, sentinel) || out != "operation failed (details redacted)" {
		t.Fatalf("classification was not strictly redacted: %q", out)
	}
	if stderr := errorMessage(errors.New("provider body: " + sentinel)); strings.Contains(stderr, sentinel) || stderr != "credential loader: operation failed (details redacted)" {
		t.Fatalf("stderr was not strictly redacted: %q", stderr)
	}
}

func TestLoadCredentialRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("synthetic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential(path); err == nil {
		t.Fatal("broad permissions accepted")
	}
}

func TestDeletePinsReceiptUIDAndLeavesReplacement(t *testing.T) {
	state, receipt, kubeconfig := privateState(t)
	_ = state
	const name = "mecak8s-live-replacement-test"
	originalUID := types.UID("original-synthetic-uid")
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		created := action.(ktesting.CreateAction).GetObject().(*corev1.Secret).DeepCopy()
		created.UID = originalUID
		return true, created, nil
	})
	if err := stageSecret(context.Background(), client.CoreV1().Secrets(namespace), receipt, kubeconfig, "kind-owned", name, []byte("synthetic-sentinel")); err != nil {
		t.Fatal(err)
	}

	replacementUID := types.UID("replacement-synthetic-uid")
	client.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(ktesting.DeleteAction)
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != originalUID {
			t.Fatal("cleanup did not pin the staged UID")
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, name, errors.New("UID precondition does not match"))
	})
	if err := deleteStagedSecret(context.Background(), client.CoreV1().Secrets(namespace), receipt, kubeconfig, "kind-owned", name); err == nil {
		t.Fatal("replacement UID mismatch was accepted")
	}
	if _, err := os.Stat(receipt); err != nil {
		t.Fatal("receipt was removed despite UID mismatch")
	}
	_ = replacementUID // The simulated replacement identity must never be targeted.
	assertNoSecretReads(t, client.Actions())
}

func TestDeleteTreatsNotFoundAsCleaned(t *testing.T) {
	_, receipt, kubeconfig := privateState(t)
	const name = "mecak8s-live-not-found-test"
	uid := types.UID("staged-synthetic-uid")
	if err := writeReceipt(receipt, cleanupReceipt{Name: name, Namespace: namespace, UID: uid, Context: "kind-owned", Kubeconfig: kubeconfig}); err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset()
	client.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		preconditions := action.(ktesting.DeleteAction).GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != uid {
			t.Fatal("cleanup did not pin the staged UID")
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
	})
	if err := deleteStagedSecret(context.Background(), client.CoreV1().Secrets(namespace), receipt, kubeconfig, "kind-owned", name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful cleanup retained its receipt")
	}
	assertNoSecretReads(t, client.Actions())
}

func TestReceiptIsPrivateAndCreateOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt")
	receipt := cleanupReceipt{Name: "mecak8s-live-receipt-test", Namespace: namespace, UID: "synthetic-uid", Context: "kind-owned", Kubeconfig: "/private/kubeconfig"}
	if err := writeReceipt(path, receipt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("receipt was not created with mode 0600")
	}
	if err := writeReceipt(path, receipt); err == nil {
		t.Fatal("receipt overwrite was accepted")
	}
}

func TestStageFailureDoesNotDeleteOrWriteReceipt(t *testing.T) {
	_, receipt, kubeconfig := privateState(t)
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "mecak8s-live-stage-failure")
	})
	err := stageSecret(context.Background(), client.CoreV1().Secrets(namespace), receipt, kubeconfig, "kind-owned", "mecak8s-live-stage-failure", []byte("synthetic-sentinel"))
	if err == nil {
		t.Fatal("stage failure was accepted")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stage failure wrote a cleanup receipt")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("stage failure attempted deletion")
		}
	}
	assertNoSecretReads(t, client.Actions())
}

func TestMissingUIDDoesNotWriteReceiptOrDeleteByName(t *testing.T) {
	_, receipt, kubeconfig := privateState(t)
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: namespace}})
	name := "mecak8s-live-ambiguous-test"
	if err := stageSecret(context.Background(), client.CoreV1().Secrets(namespace), receipt, kubeconfig, "kind-owned", name, []byte("synthetic-sentinel")); err == nil || err.Error() != "secret staged without UID; cleanup pending" {
		t.Fatalf("missing UID result = %v", err)
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing UID wrote an invalid cleanup receipt")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("missing UID triggered name-only deletion")
		}
	}
	assertNoSecretReads(t, client.Actions())
}

func privateState(t *testing.T) (string, string, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(state, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state, filepath.Join(state, "live-secret-test.receipt.json"), kubeconfig
}

func assertNoSecretReads(t *testing.T, actions []ktesting.Action) {
	t.Helper()
	for _, action := range actions {
		if action.GetResource().Resource == "secrets" && (action.GetVerb() == "get" || action.GetVerb() == "list") {
			t.Fatalf("unexpected Secret %s action", action.GetVerb())
		}
	}
}
