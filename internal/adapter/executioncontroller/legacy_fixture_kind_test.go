//go:build kind_execution_e2e

package executioncontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestLegacyQuotaWaitCoversDefaultControllerResync(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	kube := kubefake.NewClientset()
	kube.PrependReactor("get", "resourcequotas", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, nil, context.Canceled
	})
	if legacyQuotaWait < 6*time.Minute {
		t.Fatal("fixture must cover the five-minute quota resync plus margin")
	}
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	_, err := seedOneLegacyEnvironment(ctx, d, kube, "test", resolvedProfile{}, executionenv.Owner{}, "client", "legacy", "binding", nil, false)
	if !errors.Is(err, context.Canceled) || len(d.Actions()) != 0 {
		t.Fatal("cancellation must stop before creating fixtures")
	}
}

func TestLegacyFixtureWaitsForQuotaAccountingBeforeCreate(t *testing.T) {
	for _, mode := range []string{"delayed", "timeout", "cancel", "forbidden", "api-error"} {
		t.Run(mode, func(t *testing.T) {
			quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "mecatl-execution", Namespace: "test"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceName("count/executionenvironments.execution.mecatl.dev"): resource.MustParse("10"), corev1.ResourcePods: resource.MustParse("10"), corev1.ResourceName("private-quota-key"): resource.MustParse("12345")}}}
			kube := kubefake.NewClientset()
			reads, creates := 0, 0
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			const hostile = "https://sentinel.invalid/private?token=sk-fake-quota-secret"
			kube.PrependReactor("get", "resourcequotas", func(ktesting.Action) (bool, runtime.Object, error) {
				reads++
				if mode == "forbidden" {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "resourcequotas"}, quota.Name, errors.New(hostile))
				}
				if mode == "api-error" {
					return true, nil, apierrors.NewInternalError(errors.New(hostile))
				}
				// Partially initialized status must not pass: all configured resources matter.
				quota.Status.Hard = quota.Spec.Hard.DeepCopy()
				quota.Status.Used = corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0")}
				if mode == "timeout" {
					quota.Status.Hard[corev1.ResourcePods] = resource.MustParse("20")
				}
				if mode == "delayed" && reads >= 2 {
					quota.Status.Used = quota.Spec.Hard.DeepCopy()
				}
				if mode == "cancel" {
					cancel()
				}
				return true, quota.DeepCopy(), nil
			})
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			stop := errors.New("create reached")
			d.PrependReactor("create", "executionenvironments", func(ktesting.Action) (bool, runtime.Object, error) {
				creates++
				if reads < 2 || mode != "delayed" {
					t.Error("create before quota accounting initialized")
				}
				return true, nil, stop
			})
			_, err := seedOneLegacyEnvironment(ctx, d, kube, "test", resolvedProfile{}, executionenv.Owner{}, "client", "legacy", "binding", nil, false)
			if mode == "delayed" {
				if !errors.Is(err, stop) || creates != 1 {
					t.Fatalf("creates=%d reads=%d error=%v", creates, reads, err)
				}
				return
			}
			if err == nil || creates != 0 {
				t.Fatalf("creates=%d error=%v", creates, err)
			}
			if mode == "forbidden" || mode == "api-error" {
				var reported strings.Builder
				fmt.Fprintln(&reported, err) // Same reporting boundary as legacyfixture.fail.
				for _, secret := range []string{"https://sentinel.invalid", "sk-fake-quota-secret"} {
					if strings.Contains(err.Error(), secret) || strings.Contains(reported.String(), secret) {
						t.Fatal("quota error disclosed API response data")
					}
				}
				if reads != 1 {
					t.Fatalf("API error retried: reads=%d", reads)
				}
			}
			switch mode {
			case "timeout":
				if strings.Contains(err.Error(), "private-quota-key") || strings.Contains(err.Error(), "12345") || !strings.Contains(err.Error(), "pods") {
					t.Fatalf("quota diagnostics lost mismatch or leaked data: %v", err)
				}
				if !strings.Contains(err.Error(), "count/executionenvironments.execution.mecatl.dev") || !strings.Contains(err.Error(), "elapsed=") {
					t.Fatalf("missing bounded quota diagnostic: %v", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "forbidden":
				if !apierrors.IsForbidden(err) || reads != 1 {
					t.Fatalf("reads=%d error=%v", reads, err)
				}
			}
		})
	}
}
