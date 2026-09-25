package server

import (
	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

// DownloadArtifact streams a caller-owned PDF in bounded frames. Session ownership
// and artifact lookup complete before the first Send.
func (h *HarnessServer) DownloadArtifact(req *mecatlv1.DownloadArtifactRequest, stream grpc.ServerStreamingServer[mecatlv1.DownloadArtifactResponse]) error {
	ctx := stream.Context()
	if err := validateGRPCSessionAffinity(ctx, req.GetSessionId()); err != nil {
		return err
	}
	meta, reader, err := h.svc.DownloadArtifact(ctx, session.SessionID(req.GetSessionId()), req.GetArtifactId())
	if err != nil {
		return toStatus(err)
	}
	err = streamArtifact(ctx, meta, reader, func(chunk []byte) error {
		return stream.Send(&mecatlv1.DownloadArtifactResponse{Chunk: chunk})
	})
	if err != nil {
		return toStatus(err)
	}
	return nil
}
