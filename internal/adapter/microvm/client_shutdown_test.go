package microvm

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestShutdownDaemonSendsExactIdentityAndRequiresAcknowledgement(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	expected := DaemonInfo{
		ProtocolVersion: protocolVersion,
		ReleaseIdentity: "release-v1", BinaryIdentity: "sha256:binary", ConfigDigest: "sha256:config",
		PolicyRevision: "policy-v1", Profiles: []string{"microvm-local"}, Socket: socket,
	}
	requestCh := make(chan lifecycleRequest, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request lifecycleRequest
		if readFrame(conn, &request) == nil {
			requestCh <- request
			_ = writeFrame(conn, lifecycleResponse{})
		}
	}()
	client, err := New("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ShutdownDaemon(t.Context(), expected); err != nil {
		t.Fatalf("ShutdownDaemon: %v", err)
	}
	request := <-requestCh
	if request.Operation != "shutdown" || request.Version != protocolVersion || request.Binding != (binding{}) || request.Provision != nil {
		t.Fatalf("shutdown request = %+v", request)
	}
	var got DaemonInfo
	if err := json.Unmarshal(request.Payload, &got); err != nil || !got.Equal(expected) {
		t.Fatalf("shutdown identity = %+v, %v; want %+v", got, err, expected)
	}
}

func TestShutdownDaemonPropagatesServerFailureAndRejectsMalformedAck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response lifecycleResponse
		want     string
	}{
		{name: "server rejection", response: lifecycleResponse{ErrorCode: "failed_precondition", ErrorText: "identity mismatch"}, want: "identity mismatch"},
		{name: "malformed acknowledgement", response: lifecycleResponse{Payload: json.RawMessage(`{"unexpected":true}`)}, want: "malformed shutdown acknowledgement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := testUnixSocketPath(t)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				var request lifecycleRequest
				_ = readFrame(conn, &request)
				_ = writeFrame(conn, tc.response)
			}()
			client, _ := New("unix://" + socket)
			expected := DaemonInfo{
				ProtocolVersion: protocolVersion,
				ReleaseIdentity: "release-v1", BinaryIdentity: "sha256:binary", ConfigDigest: "sha256:config",
				PolicyRevision: "policy-v1", Profiles: []string{"microvm-local"}, Socket: socket,
			}
			if err := client.ShutdownDaemon(t.Context(), expected); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ShutdownDaemon error = %v, want %q", err, tc.want)
			}
		})
	}
}
