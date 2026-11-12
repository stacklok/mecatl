package executioncontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/engine/port"
)

type recordedDiagnostics struct {
	mu      sync.Mutex
	records []string
}

func (d *recordedDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, fmt.Sprint(append([]any{msg}, args...)...))
}

func (d *recordedDiagnostics) With(...any) port.Diagnostics { return d }

type certificateFixture struct {
	dynamic *dynamicfake.FakeDynamicClient
	kube    *kubefake.Clientset
	r       *Reconciler
	diag    *recordedDiagnostics
	env     string
}

func newCertificateFixture(t *testing.T) *certificateFixture {
	t.Helper()
	env := testEnvironment()
	env.SetFinalizers([]string{environmentFinalizer})
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	var pods atomic.Int32
	k.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch obj := action.(k8stesting.CreateAction).GetObject().(type) {
		case *corev1.PersistentVolumeClaim:
			obj.UID = "pvc-1"
		case *corev1.Pod:
			obj.UID = types.UID(fmt.Sprintf("pod-%d", pods.Add(1)))
			obj.Status.Conditions = []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			}
		case *corev1.Secret:
			obj.UID = types.UID("secret-" + obj.Name)
		}
		return false, nil, nil
	})
	diag := &recordedDiagnostics{}
	r := NewReconciler(d, k, "ns", testProfiles()).WithDiagnostics(diag)
	t.Cleanup(r.queue.ShutDown)
	return &certificateFixture{dynamic: d, kube: k, r: r, diag: diag, env: env.GetName()}
}

func (f *certificateFixture) environments() dynamic.ResourceInterface {
	return f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns")
}

func (f *certificateFixture) podClient() typedcorev1.PodInterface {
	return f.kube.CoreV1().Pods("ns")
}

func (f *certificateFixture) secretClient() typedcorev1.SecretInterface {
	return f.kube.CoreV1().Secrets("ns")
}

func (f *certificateFixture) get(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	got, err := f.environments().Get(t.Context(), f.env, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *certificateFixture) executorPod(t *testing.T) *corev1.Pod {
	t.Helper()
	pod, err := f.podClient().Get(t.Context(), resourceName("executor", f.env), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return pod
}

func (f *certificateFixture) secrets(t *testing.T) []corev1.Secret {
	t.Helper()
	list, err := f.secretClient().List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func (f *certificateFixture) reconcile(t *testing.T) {
	t.Helper()
	if err := f.r.Reconcile(t.Context(), f.env); err != nil {
		t.Fatal(err)
	}
}

func (f *certificateFixture) setStatus(t *testing.T, mutate func(*unstructured.Unstructured)) {
	t.Helper()
	got := f.get(t)
	mutate(got)
	_, err := f.environments().UpdateStatus(t.Context(), got, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutorCertificateIsPinnedBeforePodAndBoundToItsUID(t *testing.T) {
	f := newCertificateFixture(t)
	f.kube.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		pin := readServerCertificatePin(f.get(t))
		if pin.Fingerprint == "" || pin.PodUID != "" {
			return true, nil, errors.New("pod created before its certificate was pinned")
		}
		return false, nil, nil
	})
	f.reconcile(t)
	got := f.get(t)
	pin := readServerCertificatePin(got)
	secrets := f.secrets(t)
	if len(secrets) != 1 || pin.PodUID != "pod-1" ||
		textNested(got.Object, "status", "pod", "uid") != "pod-1" {
		t.Fatalf("pin=%+v secrets=%d", pin, len(secrets))
	}
	secret := secrets[0]
	if secret.Name != pin.SecretName ||
		secretFingerprint(&secret) != pin.Fingerprint ||
		secret.Type != corev1.SecretTypeTLS ||
		secret.Immutable == nil || !*secret.Immutable ||
		len(secret.Data[corev1.TLSPrivateKeyKey]) == 0 {
		t.Fatalf("secret does not hold the pinned certificate: %+v", secret.ObjectMeta)
	}
	if len(secret.OwnerReferences) != 1 ||
		secret.OwnerReferences[0].UID != got.GetUID() ||
		!executorSecretBelongs(got, &secret) {
		t.Fatalf("secret ownership: %+v", secret.ObjectMeta)
	}
	pod := f.executorPod(t)
	if !validExecutorTLSVolumes(pod.Spec.Volumes[2:], pin.SecretName, testExecutorTrust()) {
		t.Fatalf("pod does not mount the pinned Secret and provider trust: %+v", pod.Spec.Volumes)
	}
	raw, _ := json.Marshal(got.Object)
	key := string(secret.Data[corev1.TLSPrivateKeyKey])
	cert := string(secret.Data[corev1.TLSCertKey])
	for _, record := range append(f.diag.records, string(raw)) {
		if strings.Contains(record, "PRIVATE KEY") ||
			strings.Contains(record, key) ||
			strings.Contains(record, cert) {
			t.Fatalf("key material leaked: %s", record)
		}
	}
}

func TestExecutorCertificateSurvivesContainerRestart(t *testing.T) {
	f := newCertificateFixture(t)
	f.reconcile(t)
	before := readServerCertificatePin(f.get(t))
	pod := f.executorPod(t)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "executor", RestartCount: 3}}
	if _, err := f.podClient().UpdateStatus(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	if after := readServerCertificatePin(f.get(t)); after != before || len(f.secrets(t)) != 1 {
		t.Fatalf("restart changed the pin: %+v -> %+v", before, after)
	}
}

func TestPodlessPinIsReusedOrReissuedOnlyWhenItsSecretIsGone(t *testing.T) {
	f := newCertificateFixture(t)
	env := f.get(t)
	first, err := f.r.ensureServerCertificate(t.Context(), env)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.r.ensureServerCertificate(t.Context(), f.get(t))
	if err != nil || again != first || len(f.secrets(t)) != 1 {
		t.Fatalf("pod-less pin was not reused: %+v %+v %v", first, again, err)
	}
	err = f.secretClient().Delete(t.Context(), first.SecretName, metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reissued, err := f.r.ensureServerCertificate(t.Context(), f.get(t))
	if err != nil || reissued.Fingerprint == first.Fingerprint ||
		readServerCertificatePin(f.get(t)) != reissued {
		t.Fatalf("missing pinned Secret was not reissued: %+v %v", reissued, err)
	}
	other, _, _, _ := generateExecutorCertificate(f.env, f.r.now())
	tampered, _ := f.secretClient().Get(t.Context(), reissued.SecretName, metav1.GetOptions{})
	tampered.Data[corev1.TLSCertKey] = other
	_, err = f.secretClient().Update(t.Context(), tampered, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.ensureServerCertificate(t.Context(), f.get(t)); err == nil {
		t.Fatal("Secret not matching its pin was accepted")
	}
	f.setStatus(t, func(o *unstructured.Unstructured) {
		pin := reissued
		pin.PodUID = "old-pod"
		writeServerCertificatePin(o, pin)
	})
	if _, err := f.r.ensureServerCertificate(t.Context(), f.get(t)); err == nil {
		t.Fatal("pin of a previous Pod was reused for a new Pod")
	}
}

func TestLostPinWriteRemovesTheIssuedSecret(t *testing.T) {
	f := newCertificateFixture(t)
	failUpdate := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("status unavailable")
	}
	f.dynamic.PrependReactor("update", "executionenvironments", failUpdate)
	if _, err := f.r.ensureServerCertificate(t.Context(), f.get(t)); err == nil {
		t.Fatal("pin write failure ignored")
	}
	if n := len(f.secrets(t)); n != 0 {
		t.Fatalf("orphaned Secrets: %d", n)
	}
}

func TestPrunedPinReportsOutdatedCRD(t *testing.T) {
	f := newCertificateFixture(t)
	prune := func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "status" {
			obj := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
			unstructured.RemoveNestedField(obj.Object, statusField, serverCertificateField)
		}
		return false, nil, nil
	}
	f.dynamic.PrependReactor("update", "executionenvironments", prune)
	if err := f.r.Reconcile(t.Context(), f.env); !errors.Is(err, errServerCertificatePruned) {
		t.Fatalf("reconcile error = %v", err)
	}
	if n := len(f.secrets(t)); n != 0 {
		t.Fatalf("orphaned Secrets: %d", n)
	}
	_, err := f.podClient().Get(t.Context(), resourceName("executor", f.env), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("executor Pod created without a stored pin: %v", err)
	}
	conditions, _, _ := unstructured.NestedSlice(f.get(t).Object, statusField, "conditions")
	for _, c := range conditions {
		condition := c.(map[string]any)
		message := errServerCertificatePruned.Error()
		if condition["type"] == "Ready" && condition["message"] == message {
			return
		}
	}
	t.Fatalf("Ready condition does not name the outdated CRD: %v", conditions)
}

func TestExecutorDriftCheckCoversCertificateAndTrustVolumes(t *testing.T) {
	f := newCertificateFixture(t)
	f.reconcile(t)
	env := f.get(t)
	profiles := testProfiles()
	profile, _ := profiles.get("go")
	pod := f.executorPod(t)
	inputs := executorPodInputs{
		pvc:            resourceName("workspace", f.env),
		serviceAccount: profiles.executorServiceAccount,
		tlsSecret:      readServerCertificatePin(env).SecretName,
		trust:          testExecutorTrust(),
	}
	if err := validatePod(env, profile, inputs, pod); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"missing certificate volume": func(p *corev1.Pod) { p.Spec.Volumes = p.Spec.Volumes[:2] },
		"other Secret": func(p *corev1.Pod) {
			p.Spec.Volumes[2].Secret.SecretName = "executor-tls-other"
		},
		"key mode": func(p *corev1.Pod) {
			p.Spec.Volumes[2].Secret.DefaultMode = new(int32(0o444))
		},
		"selected Secret items": func(p *corev1.Pod) {
			p.Spec.Volumes[2].Secret.Items = []corev1.KeyToPath{{Key: "tls.key", Path: "tls.crt"}}
		},
		"other CA Secret": func(p *corev1.Pod) {
			p.Spec.Volumes[3].Projected.Sources[0].Secret.Name = "foreign-ca"
		},
		"other CA key": func(p *corev1.Pod) {
			p.Spec.Volumes[3].Projected.Sources[0].Secret.Items[0].Key = "tls.key"
		},
		"other URI ConfigMap": func(p *corev1.Pod) {
			p.Spec.Volumes[3].Projected.Sources[1].ConfigMap.Name = "foreign"
		},
		"subPath mount": func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts[3].SubPath = "ca.crt"
		},
		"writable mount": func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts[2].ReadOnly = false
		},
		"moved mount": func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts[2].MountPath = "/workspace/tls"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			mutate(changed)
			if err := validatePod(env, profile, inputs, changed); err == nil {
				t.Fatal("drifted executor TLS volume accepted")
			}
		})
	}
	f.setStatus(t, func(o *unstructured.Unstructured) {
		pin := readServerCertificatePin(o)
		pin.SecretName = executorTLSSecretName(f.env, strings.Repeat("0", 64))
		writeServerCertificatePin(o, pin)
	})
	if err := f.r.Reconcile(t.Context(), f.env); err == nil {
		t.Fatal("executor mounting a Secret other than the pin was adopted")
	}
}

func terminateForLifecycle(t *testing.T, f *certificateFixture, kind string) {
	t.Helper()
	pod := f.executorPod(t)
	pod.Status = corev1.PodStatus{Phase: corev1.PodFailed}
	q := adminLifecycleRequest{
		ExpectedEpoch:  1,
		ExpectedPodUID: string(pod.UID),
		ExpectedPVCUID: "pvc-1",
		OperationID:    "op-" + kind,
	}
	operation := map[string]any{
		"id":             q.OperationID,
		"type":           kind,
		"phase":          "WaitingForPodDeletion",
		"expectedEpoch":  int64(1),
		"expectedPodUID": q.ExpectedPodUID,
		"expectedPVCUID": q.ExpectedPVCUID,
		"createdAt":      "2026-11-09T00:00:00Z",
	}
	proof := terminationProof(q, pod)
	f.setStatus(t, func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedMap(o.Object, proof, "status", "terminationProof")
		_ = unstructured.SetNestedMap(o.Object, operation, "status", "lifecycleOperation")
	})
	if err := f.podClient().Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorCertificateSecretsAreRemovedWithTheirPod(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		f := newCertificateFixture(t)
		f.reconcile(t)
		old := readServerCertificatePin(f.get(t))
		terminateForLifecycle(t, f, "ReplaceExecutor")
		f.reconcile(t)
		got := f.get(t)
		phase := textNested(got.Object, "status", "lifecycleOperation", "phase")
		if len(f.secrets(t)) != 0 ||
			readServerCertificatePin(got) != (serverCertificatePin{}) ||
			phase != "CreatingReplacement" {
			t.Fatalf("previous certificate outlived its Pod: %v", got.Object["status"])
		}
		f.reconcile(t)
		got = f.get(t)
		next := readServerCertificatePin(got)
		secrets := f.secrets(t)
		if len(secrets) != 1 || secrets[0].Name != next.SecretName ||
			next.Fingerprint == old.Fingerprint || next.PodUID != "pod-2" ||
			textNested(got.Object, "status", "lastReplacement", "replacementPodUID") != "pod-2" {
			t.Fatalf("replacement pin: old=%+v next=%+v secrets=%d", old, next, len(secrets))
		}
	})
	t.Run("retire and delete", func(t *testing.T) {
		f := newCertificateFixture(t)
		f.reconcile(t)
		terminateForLifecycle(t, f, "RetireEnvironment")
		f.reconcile(t)
		got := f.get(t)
		if len(f.secrets(t)) != 0 ||
			readServerCertificatePin(got) != (serverCertificatePin{}) ||
			!conditionTrue(got, "Retired") {
			t.Fatalf("retired environment kept its certificate: %v", got.Object["status"])
		}
		stale, err := f.r.ensureServerCertificate(t.Context(), got)
		if err != nil {
			t.Fatal(err)
		}
		operation := map[string]any{
			"id":             "delete",
			"type":           deleteRetiredEnvironment,
			"phase":          "DeletingPVC",
			"expectedPVCUID": "pvc-1",
			"createdAt":      "2026-11-09T00:00:00Z",
		}
		f.setStatus(t, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(o.Object, operation, "status", "lifecycleOperation")
		})
		pvcs := f.kube.CoreV1().PersistentVolumeClaims("ns")
		err = pvcs.Delete(t.Context(), resourceName("workspace", f.env), metav1.DeleteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		f.reconcile(t)
		_, err = f.secretClient().Get(t.Context(), stale.SecretName, metav1.GetOptions{})
		if err == nil {
			t.Fatal("retained deletion left a pinned certificate Secret")
		}
	})
}

func TestForeignSecretAtPinnedNameIsNotDeleted(t *testing.T) {
	f := newCertificateFixture(t)
	f.reconcile(t)
	pin := readServerCertificatePin(f.get(t))
	err := f.secretClient().Delete(t.Context(), pin.SecretName, metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pin.SecretName, Namespace: "ns"}}
	if _, err := f.secretClient().Create(t.Context(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.r.deleteServerCertificate(t.Context(), f.get(t)); err == nil {
		t.Fatal("foreign Secret deleted")
	}
	if len(f.secrets(t)) != 1 {
		t.Fatal("foreign Secret removed")
	}
}
