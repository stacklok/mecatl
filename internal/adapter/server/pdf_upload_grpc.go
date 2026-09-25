package server

import (
	"context"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	maxPDFUploadChunk                   = 256 << 10
	maxPDFUploadBytes                   = 20 << 20
	defaultArtifactUploadReceiveTimeout = 30 * time.Minute
)

func artifactUploadReceiveTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return defaultArtifactUploadReceiveTimeout
}

// UploadArtifact streams a PDF through to private storage with bounded backpressure.
func (h *HarnessServer) UploadArtifact(stream grpc.ClientStreamingServer[mecatlv1.UploadArtifactRequest, mecatlv1.UploadArtifactResponse]) error {
	ctx := stream.Context()
	if err := validateGRPCSessionAffinity(ctx, ""); err != nil {
		return err
	}
	// Start the server bound before the first Recv: an authenticated client may
	// leave even the metadata frame unsent. gRPC cancels its transport stream
	// when this handler returns, releasing the outstanding receive.
	uploadCtx, cancel := context.WithTimeout(ctx, artifactUploadReceiveTimeout(h.svc.cfg.ArtifactUploadReceiveTimeout))
	defer cancel()
	type firstResult struct {
		frame *mecatlv1.UploadArtifactRequest
		err   error
	}
	firstDone := make(chan firstResult, 1)
	go func() {
		frame, err := stream.Recv()
		firstDone <- firstResult{frame: frame, err: err}
	}()
	var first *mecatlv1.UploadArtifactRequest
	var err error
	select {
	case result := <-firstDone:
		first, err = result.frame, result.err
	case <-uploadCtx.Done():
		return status.FromContextError(uploadCtx.Err()).Err()
	}
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
	if err := validateGRPCSessionAffinity(uploadCtx, string(id)); err != nil {
		return err
	}
	// Authorize before opening a storage reader or writer, even if the stream
	// later contains malformed frames.
	if _, err := h.svc.GetSession(uploadCtx, id); err != nil {
		return toStatus(err)
	}
	if metadata.GetMimeType() != "application/pdf" {
		return status.Error(codes.InvalidArgument, "PDF upload requires application/pdf")
	}
	if h.svc.cfg.Artifacts == nil {
		return toStatus(ErrArtifactsUnavailable)
	}
	return h.stageGRPCArtifactUpload(uploadCtx, cancel, stream, id, metadata.GetName(), metadata.GetMimeType())
}

func (h *HarnessServer) stageGRPCArtifactUpload(
	uploadCtx context.Context,
	cancel context.CancelFunc,
	stream grpc.ClientStreamingServer[mecatlv1.UploadArtifactRequest, mecatlv1.UploadArtifactResponse],
	id session.SessionID,
	name, mimeType string,
) error {
	reader, writer := io.Pipe()
	// The artifact store's own timeout cannot interrupt a blocked gRPC Recv.
	type stageResult struct {
		artifact Artifact
		err      error
	}
	stageDone := make(chan stageResult, 1)
	go func() {
		artifact, stageErr := h.svc.UploadArtifact(uploadCtx, id, name, mimeType, reader)
		_ = reader.CloseWithError(stageErr)
		stageDone <- stageResult{artifact: artifact, err: stageErr}
	}()
	// gRPC cancels its transport stream when this handler returns, unblocking
	// the single outstanding Recv. The buffered result avoids a blocked sender.
	receiveDone := make(chan error, 1)
	go func() { receiveDone <- receiveArtifactUploadChunks(stream, writer) }()
	var staged *stageResult
	abort := func(err error) error {
		cancel()
		_ = writer.CloseWithError(err)
		if stageDone != nil {
			<-stageDone
		}
		return err
	}
	for {
		select {
		case receiveErr := <-receiveDone:
			receiveDone = nil
			if receiveErr != nil {
				return abort(receiveErr)
			}
			_ = writer.Close()
			if staged != nil {
				return stream.SendAndClose(&mecatlv1.UploadArtifactResponse{ArtifactId: staged.artifact.ID, Name: staged.artifact.Name, Size: staged.artifact.Size, Sha256: staged.artifact.SHA256, MimeType: staged.artifact.MIMEType})
			}
		case result := <-stageDone:
			stageDone = nil
			staged = &result
			if result.err != nil {
				if uploadCtx.Err() != nil {
					return abort(status.FromContextError(uploadCtx.Err()).Err())
				}
				return abort(toStatus(result.err))
			}
			if receiveDone == nil {
				return stream.SendAndClose(&mecatlv1.UploadArtifactResponse{ArtifactId: result.artifact.ID, Name: result.artifact.Name, Size: result.artifact.Size, Sha256: result.artifact.SHA256, MimeType: result.artifact.MIMEType})
			}
		case <-uploadCtx.Done():
			return abort(status.FromContextError(uploadCtx.Err()).Err())
		}
	}
}

func receiveArtifactUploadChunks(stream grpc.ClientStreamingServer[mecatlv1.UploadArtifactRequest, mecatlv1.UploadArtifactResponse], writer *io.PipeWriter) error {
	var total int64
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		chunkFrame, ok := frame.GetPayload().(*mecatlv1.UploadArtifactRequest_Chunk)
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
