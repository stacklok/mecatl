package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
)

// streamPDF bounds each transport frame and verifies the immutable object's
// size and digest before reporting a successful completion. It closes the
// reader on completion or cancellation, including a blocked Read.
func streamPDF(ctx context.Context, meta PDFArtifact, reader io.ReadCloser, send func([]byte) error) error {
	var closeOnce sync.Once
	closeReader := func() { closeOnce.Do(func() { _ = reader.Close() }) }
	stopClose := context.AfterFunc(ctx, closeReader)
	defer func() { stopClose(); closeReader() }()
	digest := sha256.New()
	// Hold the final byte until the object passes its size and digest check.
	// With Content-Length set, a corrupt HTTP object then ends as a truncated
	// response instead of appearing to complete successfully.
	buf := make([]byte, maxPDFUploadChunk+1)
	var total int64
	var pending byte
	hasPending := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buf[1:])
		if err := ctx.Err(); err != nil {
			return err
		}
		if n > 0 {
			total += int64(n)
			if total > maxPDFUploadBytes || total > meta.Size {
				return ErrInternal
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			_, _ = digest.Write(buf[1 : 1+n])
			if hasPending {
				buf[0] = pending
				pending = buf[n]
				if err := send(buf[:n]); err != nil {
					return err
				}
			} else {
				hasPending = true
				pending = buf[n]
				if n > 1 {
					if err := send(buf[1:n]); err != nil {
						return err
					}
				}
			}
		}
		if readErr == io.EOF {
			if !hasPending || total != meta.Size || hex.EncodeToString(digest.Sum(nil)) != meta.SHA256 {
				return ErrInternal
			}
			return send([]byte{pending})
		}
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return ErrInternal
		}
		if n == 0 {
			return ErrInternal
		}
	}
}
