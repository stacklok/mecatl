package executioncontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// PodExecutor invokes the fixed helper in an already-selected executor Pod.
type PodExecutor struct {
	config    *rest.Config
	kube      kubernetes.Interface
	namespace string
}

// NewPodExecutor constructs a namespace-scoped Pod executor transport.
func NewPodExecutor(config *rest.Config, kube kubernetes.Interface, namespace string) *PodExecutor {
	return &PodExecutor{config: rest.CopyConfig(config), kube: kube, namespace: namespace}
}

// Execute runs one bounded helper request in pod.
func (p *PodExecutor) Execute(ctx context.Context, pod string, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	body, err := json.Marshal(q)
	if err != nil {
		return executionenv.ExecutorResponse{}, err
	}
	if len(body) > executionenv.MaxJSONBody {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "executor request exceeds limit"}
	}
	req := p.kube.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(p.namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "executor", Command: []string{"/mecatl-executor"}, Stdin: true, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(p.config, "POST", req.URL())
	if err != nil {
		return executionenv.ExecutorResponse{}, fmt.Errorf("create executor stream: %w", err)
	}
	var stdout, stderr limitedBuffer
	stdout.limit = executionenv.MaxJSONBody
	stderr.limit = 64 << 10
	var stdin io.Reader = bytes.NewReader(body)
	var closeInput func()
	if q.Operation == executionenv.OpCommandStart {
		stdin, closeInput = commandInput(ctx, body)
		defer closeInput()
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return executionenv.ExecutorResponse{}, fmt.Errorf("executor stream failed: %w", err)
	}
	var envelope executionenv.ExecutorEnvelope
	if err := executionenv.DecodeStrict(stdout.Bytes(), &envelope); err != nil {
		return executionenv.ExecutorResponse{}, fmt.Errorf("invalid executor response: %w", err)
	}
	if envelope.Error != nil && envelope.Response != nil {
		return executionenv.ExecutorResponse{}, errors.New("executor returned both result and error")
	}
	if envelope.Error != nil {
		return executionenv.ExecutorResponse{}, envelope.Error
	}
	if envelope.Response == nil {
		return executionenv.ExecutorResponse{}, errors.New("executor returned no result")
	}
	return *envelope.Response, nil
}

func commandInput(ctx context.Context, body []byte) (io.Reader, func()) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		payload := append(append(make([]byte, 0, len(body)+1), body...), '\n')
		if _, err := writer.Write(payload); err != nil {
			_ = writer.CloseWithError(err)
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = writer.CloseWithError(ctx.Err())
		case <-done:
		}
	}()
	var once sync.Once
	return reader, func() {
		once.Do(func() {
			close(done)
			_ = writer.Close()
			_ = reader.Close()
		})
	}
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		b.overflow = true
		return len(p), errors.New("executor output exceeds limit")
	}
	return b.Buffer.Write(p)
}

var _ ExecutorTransport = (*PodExecutor)(nil)
