package executioncontroller

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	executorServerPortName = "executor"
	executorHealthPortName = "health"
)

const (
	keepaliveIntervalArg = "-" + executionenv.KeepaliveIntervalFlag + "="
	keepaliveTimeoutArg  = "-" + executionenv.KeepaliveTimeoutFlag + "="
)

var healthListenArg = "-" + executionenv.HealthListenFlag + "=:" +
	strconv.Itoa(executionenv.ExecutorHealthPort)

func executorServerCommand() []string {
	return []string{"/mecatl-executor", "serve"}
}

// executorServerArgs enables only the admitted services.
func executorServerArgs(
	admitted []string,
	maxFileBytes int64,
	keepalive executionenv.ExecutorKeepalive,
) []string {
	args := []string{"-listen=:" + strconv.Itoa(executionenv.ExecutorServerPort)}
	if slices.Contains(admitted, CapabilityFilesystem) {
		args = append(args, "-files")
	}
	if slices.Contains(admitted, CapabilityForegroundCommand) {
		args = append(args, "-commands")
	}
	return append(args,
		"-max-file-bytes="+strconv.FormatInt(maxFileBytes, 10),
		keepaliveIntervalArg+keepalive.Interval.String(),
		keepaliveTimeoutArg+keepalive.Timeout.String(),
		"-tls-dir="+serverTLSMountPath,
		"-provider-trust-dir="+providerTrustMountPath,
		healthListenArg,
	)
}

func executorServerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{{
		Name:          executorServerPortName,
		ContainerPort: executionenv.ExecutorServerPort,
		Protocol:      corev1.ProtocolTCP,
	}, {
		Name:          executorHealthPortName,
		ContainerPort: executionenv.ExecutorHealthPort,
		Protocol:      corev1.ProtocolTCP,
	}}
}

// executorReadinessProbe spells out every field the API server would default,
// so the created Pod compares equal. The kubelet's gRPC probe has no TLS, so
// it calls the plaintext health server, which reports /workspace writability.
func executorReadinessProbe() *corev1.Probe {
	service := ""
	return withProbeTiming(corev1.ProbeHandler{GRPC: &corev1.GRPCAction{
		Port:    executionenv.ExecutorHealthPort,
		Service: &service,
	}})
}

// legacyReadinessProbe only connects to the server port, as on Pods created
// before the health server.
func legacyReadinessProbe() *corev1.Probe {
	return withProbeTiming(corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{
		Port: intstr.FromInt32(executionenv.ExecutorServerPort),
	}})
}

func withProbeTiming(handler corev1.ProbeHandler) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     handler,
		PeriodSeconds:    2,
		TimeoutSeconds:   1,
		SuccessThreshold: 1,
		FailureThreshold: 3,
	}
}

// validExecutorServer also accepts a Pod created before the health server, so
// existing environments survive an upgrade.
func validExecutorServer(c *corev1.Container, args []string) bool {
	legacyArgs := slices.DeleteFunc(slices.Clone(args), func(arg string) bool {
		return arg == healthListenArg
	})
	return reflect.DeepEqual(c.Command, executorServerCommand()) &&
		len(args) != 0 &&
		(serverShape(c, args, executorServerPorts(), executorReadinessProbe()) ||
			serverShape(c, legacyArgs, executorServerPorts()[:1], legacyReadinessProbe())) &&
		c.LivenessProbe == nil &&
		c.StartupProbe == nil &&
		c.Lifecycle == nil
}

func serverShape(
	c *corev1.Container,
	args []string,
	ports []corev1.ContainerPort,
	probe *corev1.Probe,
) bool {
	return reflect.DeepEqual(withoutKeepalive(c.Args), withoutKeepalive(args)) &&
		reflect.DeepEqual(c.Ports, ports) &&
		reflect.DeepEqual(c.ReadinessProbe, probe)
}

// withoutKeepalive drops one in-range value per flag, so existing Pods keep their timing.
func withoutKeepalive(args []string) []string {
	seen := map[string]bool{}
	return slices.DeleteFunc(slices.Clone(args), func(arg string) bool {
		for _, flag := range []string{keepaliveIntervalArg, keepaliveTimeoutArg} {
			value, ok := strings.CutPrefix(arg, flag)
			if !ok {
				continue
			}
			d, err := time.ParseDuration(value)
			if err != nil || seen[flag] || !executionenv.ExecutorKeepaliveInRange(d) {
				return false
			}
			seen[flag] = true
			return true
		}
		return false
	})
}
