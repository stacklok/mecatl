package server

import (
	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

// DownloadPdf streams a caller-owned PDF in bounded frames. Session ownership
// and artifact lookup complete before the first Send.
func (h *HarnessServer) DownloadPdf(req *mecatlv1.DownloadPdfRequest, stream grpc.ServerStreamingServer[mecatlv1.DownloadPdfResponse]) error {
	ctx := stream.Context()
	if err := validateGRPCSessionAffinity(ctx, req.GetSessionId()); err != nil {
		return err
	}
	meta, reader, err := h.svc.DownloadPdf(ctx, session.SessionID(req.GetSessionId()), req.GetArtifactId())
	if err != nil {
		return toStatus(err)
	}
	err = streamPDF(ctx, meta, reader, func(chunk []byte) error {
		return stream.Send(&mecatlv1.DownloadPdfResponse{Chunk: chunk})
	})
	if err != nil {
		return toStatus(err)
	}
	return nil
}
