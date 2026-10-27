package client

import (
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recvSide and sendSide are the two halves of a bidirectional stream; a
// generated grpc.BidiStreamingClient satisfies both.
type (
	recvSide[Resp any] interface{ Recv() (*Resp, error) }
	sendSide[Req any]  interface{ Send(*Req) error }
)

// reopenableStream is the transport shared by a Converse stream and the MCP
// authorization-control streams. If the server rejects the bearer
// (Unauthenticated) before any response, reopen replaces the stream once with a
// freshly refreshed token, and the frames sent so far are replayed: the server
// processed none of them. A second rejection, or one after a response, is
// returned.
type reopenableStream[Req, Resp any] struct {
	// recv belongs to the reader goroutine, which alone replaces it.
	recv recvSide[Resp]

	mu   sync.Mutex // serialises Send; guards send, reopen, recording, unanswered
	send sendSide[Req]
	// reopen, when set, is used at most once. Until the first response, sent
	// frames are kept in unanswered so the replacement can replay them.
	reopen     func() (recvSide[Resp], sendSide[Req], error)
	recording  bool
	unanswered []*Req
	// answered is set by the reader goroutine alone, after the first response.
	answered bool
	// onAnswered, when set before the read loop starts, runs once with the
	// first response.
	onAnswered func()
}

// armReopen enables the one-time reopen after a rejected bearer.
func (s *reopenableStream[Req, Resp]) armReopen(reopen func() (recvSide[Resp], sendSide[Req], error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reopen = reopen
	s.recording = true
}

// receive returns the next response, reopening once as described on the type.
func (s *reopenableStream[Req, Resp]) receive() (*Resp, error) {
	resp, err := s.recv.Recv()
	if err != nil && !s.answered && status.Code(err) == codes.Unauthenticated {
		if reopened, reopenErr := s.reopenOnce(); reopened {
			resp, err = s.recv.Recv()
		} else if _, local := localAuthFailure(reopenErr); local {
			err = reopenErr // e.g. the session can no longer refresh
		}
	}
	if err == nil && !s.answered {
		s.answered = true
		s.mu.Lock()
		s.recording = false
		s.unanswered = nil
		s.mu.Unlock()
		if s.onAnswered != nil {
			s.onAnswered()
		}
	}
	return resp, err
}

// reopenOnce replaces the rejected stream and reports whether it did. Frames
// sent while the replacement is opening still go to the rejected stream and
// are recorded, so the replay under mu sends each frame once.
func (s *reopenableStream[Req, Resp]) reopenOnce() (bool, error) {
	s.mu.Lock()
	reopen := s.reopen
	s.reopen = nil
	s.mu.Unlock()
	if reopen == nil {
		return false, nil
	}
	recv, send, err := reopen()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recording = false
	frames := s.unanswered
	s.unanswered = nil
	if err != nil {
		return false, err
	}
	for _, frame := range frames {
		if send.Send(frame) != nil {
			break // the replacement's Recv reports why
		}
	}
	s.recv, s.send = recv, send
	return true, nil
}

// sendFrame serialises one Send under the mutex. While a reopen is still
// possible the frame is recorded for replay, and io.EOF (the stream already
// ended) is not an error: the reader receives the stream's real status and
// either reopens and replays the frame or reports that status.
func (s *reopenableStream[Req, Resp]) sendFrame(req *Req) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.recording {
		return s.send.Send(req)
	}
	s.unanswered = append(s.unanswered, req)
	if err := s.send.Send(req); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
