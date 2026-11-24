package executioncontroller

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func testServerArgs() []string {
	return []string{
		"-listen=:7443",
		"-files",
		"-commands",
		"-max-file-bytes=1024",
		"-keepalive-interval=10s",
		"-keepalive-timeout=10s",
		"-tls-dir=/etc/mecatl-executor/tls",
		"-provider-trust-dir=/etc/mecatl-executor/provider",
	}
}

func TestExecutorPodRunsTheServer(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	k := kubefake.NewSimpleClientset()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(d, k, "ns", testProfiles())
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	wantArgs := testServerArgs()
	wantPorts := []corev1.ContainerPort{
		{Name: "executor", ContainerPort: 7443, Protocol: corev1.ProtocolTCP},
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways ||
		!reflect.DeepEqual(c.Command, []string{"/mecatl-executor", "serve"}) ||
		!reflect.DeepEqual(c.Args, wantArgs) ||
		!reflect.DeepEqual(c.Ports, wantPorts) {
		t.Fatalf("executor does not run the server: policy=%s command=%q args=%q ports=%+v",
			pod.Spec.RestartPolicy, c.Command, c.Args, c.Ports)
	}
	probe := c.ReadinessProbe
	if probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port != intstr.FromInt32(7443) {
		t.Fatalf("readiness probe does not check the server port: %+v", probe)
	}
}

func TestExecutorServerEnablesOnlyAdmittedServices(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	env.Object["spec"].(map[string]any)[admittedCapabilitiesField] = []any{CapabilityFilesystem}
	k := kubefake.NewSimpleClientset()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(d, k, "ns", testProfiles())
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := slices.DeleteFunc(testServerArgs(), func(a string) bool { return a == "-commands" })
	if got := pod.Spec.Containers[0].Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("file-only executor args = %q, want %q", got, want)
	}
	profile, _ := testProfiles().get("go")
	inputs := testPodInputs(testProfiles(), pod)
	inputs.serverArgs = want
	if err := validatePod(env, profile, inputs, pod); err != nil {
		t.Fatal(err)
	}
	widened := pod.DeepCopy()
	widened.Spec.Containers[0].Args = testServerArgs()
	if err := validatePod(env, profile, inputs, widened); err == nil {
		t.Fatal("drift check accepted Commands on a file-only executor")
	}
}

func TestExecutorKeepaliveReachesNewPodsAndSparesExistingOnes(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	k := kubefake.NewSimpleClientset()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	keepalive := executionenv.ExecutorKeepalive{
		Interval: 30 * time.Second,
		Timeout:  5 * time.Second,
	}
	profiles := testProfiles().WithExecutorKeepalive(keepalive)
	r := NewReconciler(d, k, "ns", profiles)
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	args := pod.Spec.Containers[0].Args
	if !slices.Contains(args, keepaliveIntervalArg+"30s") ||
		!slices.Contains(args, keepaliveTimeoutArg+"5s") {
		t.Fatalf("executor args = %q", args)
	}
	profile, _ := profiles.get("go")
	if err := validatePod(env, profile, testPodInputs(profiles, pod), pod); err != nil {
		t.Fatalf("keepalive change invalidated the existing Pod: %v", err)
	}
}

func TestExecutorDriftCheckRejectsTamperedKeepalive(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	k := kubefake.NewSimpleClientset()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	profiles := testProfiles()
	r := NewReconciler(d, k, "ns", profiles)
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := profiles.get("go")
	inputs := testPodInputs(profiles, pod)
	withArgs := func(args ...string) *corev1.Pod {
		p := pod.DeepCopy()
		c := &p.Spec.Containers[0]
		c.Args = append(slices.DeleteFunc(slices.Clone(c.Args), func(a string) bool {
			return strings.HasPrefix(a, keepaliveIntervalArg) ||
				strings.HasPrefix(a, keepaliveTimeoutArg)
		}), args...)
		return p
	}
	if err := validatePod(env, profile, inputs, withArgs()); err != nil {
		t.Fatalf("Pod from before keepalive flags rejected: %v", err)
	}
	for name, args := range map[string][]string{
		"zero interval":     {keepaliveIntervalArg + "0s"},
		"over the maximum":  {keepaliveTimeoutArg + "1h"},
		"unparsable":        {keepaliveIntervalArg + "soon"},
		"repeated interval": {keepaliveIntervalArg + "10s", keepaliveIntervalArg + "10s"},
	} {
		t.Run(name, func(t *testing.T) {
			p := withArgs(args...)
			if err := validatePod(env, profile, inputs, p); err == nil {
				t.Fatalf("args %q accepted", p.Spec.Containers[0].Args)
			}
		})
	}
}

func TestExecutorDriftCheckRequiresTheServer(t *testing.T) {
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
		serverArgs:     testServerArgs(),
	}
	if err := validatePod(env, profile, inputs, pod); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"sleep command": func(p *corev1.Pod) {
			p.Spec.Containers[0].Command = []string{
				"/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait",
			}
			p.Spec.Containers[0].Args = nil
		},
		"image entrypoint": func(p *corev1.Pod) {
			p.Spec.Containers[0].Command = nil
		},
		"never restart": func(p *corev1.Pod) {
			p.Spec.RestartPolicy = corev1.RestartPolicyNever
		},
		"restart on failure": func(p *corev1.Pod) {
			p.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		},
		"listen address": func(p *corev1.Pod) {
			p.Spec.Containers[0].Args[0] = "-listen=:8443"
		},
		"extra flag": func(p *corev1.Pod) {
			p.Spec.Containers[0].Args = append(p.Spec.Containers[0].Args, "-tls-dir=/workspace")
		},
		"file limit": func(p *corev1.Pod) {
			p.Spec.Containers[0].Args[3] = "-max-file-bytes=5242880"
		},
		"no port": func(p *corev1.Pod) {
			p.Spec.Containers[0].Ports = nil
		},
		"host port": func(p *corev1.Pod) {
			p.Spec.Containers[0].Ports[0].HostPort = 7443
		},
		"no readiness": func(p *corev1.Pod) {
			p.Spec.Containers[0].ReadinessProbe = nil
		},
		"readiness port": func(p *corev1.Pod) {
			p.Spec.Containers[0].ReadinessProbe.TCPSocket.Port = intstr.FromInt32(1)
		},
		"liveness probe": func(p *corev1.Pod) {
			p.Spec.Containers[0].LivenessProbe = &corev1.Probe{}
		},
		"startup probe": func(p *corev1.Pod) {
			p.Spec.Containers[0].StartupProbe = &corev1.Probe{}
		},
		"lifecycle hook": func(p *corev1.Pod) {
			p.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{}
		},
		"one-shot helper": func(p *corev1.Pod) {
			p.Spec.Containers[0].Command = []string{"/mecatl-executor"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			mutate(changed)
			if err := validatePod(env, profile, inputs, changed); err == nil {
				t.Fatal("drifted executor server accepted")
			}
		})
	}
}

func TestCrashedServerRestartsInTheSamePodAndPVC(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	r := NewReconciler(d, k, "ns", testProfiles())
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pods := k.CoreV1().Pods("ns")
	setStatus := func(ready bool, restarts int32, state corev1.ContainerState) {
		t.Helper()
		pod, err := pods.Get(ctx, "executor-test", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod.UID = types.UID("pod-uid")
		condition := corev1.ConditionFalse
		if ready {
			condition = corev1.ConditionTrue
		}
		pod.Status = corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: condition}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "executor",
				Ready:        ready,
				RestartCount: restarts,
				State:        state,
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 2},
				},
			}},
		}
		if _, err := pods.Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	reconcile := func(wantReady bool) {
		t.Helper()
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
		got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").
			Get(ctx, env.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if conditionTrue(got, "Ready") != wantReady ||
			textNested(got.Object, "status", "pod", "uid") != "pod-uid" ||
			textNested(got.Object, "status", "pvc", "name") != "workspace-test" ||
			textNested(got.Object, "status", "fenceState") != "Healthy" {
			t.Fatalf("restart changed the environment: %v", got.Object["status"])
		}
	}
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	setStatus(true, 0, running)
	reconcile(true)
	k.ClearActions()

	setStatus(false, 1, corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	})
	reconcile(false)
	setStatus(true, 1, running)
	reconcile(true)
	for _, action := range k.Actions() {
		if action.Matches("create", "pods") || action.Matches("delete", "pods") ||
			action.Matches("create", "persistentvolumeclaims") ||
			action.Matches("delete", "persistentvolumeclaims") {
			t.Fatalf("server restart replaced runtime objects: %s", describeAction(action))
		}
	}
}

func describeAction(a k8stesting.Action) string {
	return a.GetVerb() + " " + a.GetResource().Resource
}
