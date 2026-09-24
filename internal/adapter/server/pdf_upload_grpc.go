package server

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	maxPDFUploadChunk = 256 << 10
	maxPDFUploadBytes = 20 << 20
)

// UploadPdf streams a PDF through to private storage with bounded backpressure.
func (h *HarnessServer) UploadPdf(stream grpc.ClientStreamingServer[mecatlv1.UploadPdfRequest, mecatlv1.UploadPdfResponse]) error {
	ctx := stream.Context()
	if err := validateGRPCSessionAffinity(ctx, ""); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			return status.Error(codes.InvalidArgument, "PDF upload metadata is required")
		}
		return err
	}
	metadata := first.GetMetadata()
	if metadata == nil || metadata.GetSessionId() == "" || metadata.GetName() == "" {
		return status.Error(codes.InvalidArgument, "PDF upload metadata must be first")
	}
	id := session.SessionID(metadata.GetSessionId())
	if err := validateGRPCSessionAffinity(ctx, string(id)); err != nil {
		return err
	}
	// Authorize before opening a storage reader or writer, even if the stream
	// later contains malformed frames.
	if _, err := h.svc.GetSession(ctx, id); err != nil {
		return toStatus(err)
	}
	if metadata.GetMimeType() != "application/pdf" {
		return status.Error(codes.InvalidArgument, "PDF upload requires application/pdf")
	}
	if h.svc.cfg.PDFArtifacts == nil {
		return toStatus(ErrPDFArtifactsUnavailable)
	}
	reader, writer := io.Pipe()
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type stageResult struct {
		artifact PDFArtifact
		err      error
	}
	done := make(chan stageResult, 1)
	go func() {
		artifact, stageErr := h.svc.UploadPdf(uploadCtx, id, metadata.GetName(), reader)
		_ = reader.CloseWithError(stageErr)
		done <- stageResult{artifact: artifact, err: stageErr}
	}()
	fail := func(err error) error {
		cancel()
		_ = writer.CloseWithError(err)
		<-done
		return err
	}
	if err := receivePDFUploadChunks(stream, writer); err != nil {
		return fail(err)
	}
	_ = writer.Close()
	result := <-done
	if result.err != nil {
		return toStatus(result.err)
	}
	return stream.SendAndClose(&mecatlv1.UploadPdfResponse{ArtifactId: result.artifact.ID, Name: result.artifact.Name, Size: result.artifact.Size, Sha256: result.artifact.SHA256})
}

func receivePDFUploadChunks(stream grpc.ClientStreamingServer[mecatlv1.UploadPdfRequest, mecatlv1.UploadPdfResponse], writer *io.PipeWriter) error {
	var total int64
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		chunkFrame, ok := frame.GetPayload().(*mecatlv1.UploadPdfRequest_Chunk)
		if !ok || len(chunkFrame.Chunk) == 0 || len(chunkFrame.Chunk) > maxPDFUploadChunk {
			return status.Error(codes.InvalidArgument, "invalid PDF upload chunk")
		}
		total += int64(len(chunkFrame.Chunk))
		if total > maxPDFUploadBytes {
			return status.Error(codes.ResourceExhausted, "PDF upload exceeds 20 MiB")
		}
		if _, writeErr := writer.Write(chunkFrame.Chunk); writeErr != nil {
			return toStatus(ErrInvalidArgument)
		}
	}
	if total == 0 {
		return status.Error(codes.InvalidArgument, "PDF upload is empty")
	}
	return nil
}
