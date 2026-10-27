package client

import (
	"errors"
	"io"
	"sync"
)

// recvSide and sendSide are the two halves of a bidirectional stream; a
// generated grpc.BidiStreamingClient satisfies both.
type (
	recvSide[Resp any] interface{ Recv() (*Resp, error) }
	sendSide[Req any]  interface{ Send(*Req) error }
)

// reopenableStream is the transport shared by a Converse stream and the MCP
// authorization-control streams. If the server's authenticator rejects the
// bearer (isBearerRejected) before any response, reopen replaces the stream
// once with a freshly refreshed token, and the frames sent so far are
// replayed: the server processed none of them. Any other Unauthenticated, a
// second rejection, or one after a response is returned.
type reopenableStream[Req, Resp any] struct {
	// recv belongs to the reader goroutine, which alone replaces it.
	recv recvSide[Resp]

	// sendMu serialises Send, including the replay, and guards send. It is held
	// across network calls, so the reader goroutine takes it only in reopenOnce,
	// before any response: a Send blocked on flow control must not stop the
	// reads that would unblock it.
	sendMu sync.Mutex
	send   sendSide[Req]
	// mu guards reopen, recording, and unanswered and is never held across a
	// network call. Take it after sendMu, never before.
	mu sync.Mutex
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
	if err != nil && !s.answered && isBearerRejected(err) {
		if reopened, reopenErr := s.reopenOnce(); reopened {
			resp, err = s.recv.Recv()
		} else {
			err = refreshFailure(err, reopenErr)
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
// are recorded, so the replay under sendMu sends each frame once.
func (s *reopenableStream[Req, Resp]) reopenOnce() (bool, error) {
	s.mu.Lock()
	reopen := s.reopen
	s.reopen = nil
	s.mu.Unlock()
	if reopen == nil {
		return false, nil
	}
	recv, send, err := reopen()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.recording = false
	frames := s.unanswered
	s.unanswered = nil
	s.mu.Unlock()
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

// sendFrame serialises one Send under sendMu. While a reopen is still possible
// the frame is recorded for replay, and io.EOF (the stream already ended) is
// not an error: the reader receives the stream's real status and either
// reopens and replays the frame or reports that status. A frame whose Send
// failed otherwise is returned as an error, so it is not replayed.
func (s *reopenableStream[Req, Resp]) sendFrame(req *Req) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	recording := s.recording
	if recording {
		s.unanswered = append(s.unanswered, req)
	}
	s.mu.Unlock()
	err := s.send.Send(req)
	if !recording || err == nil {
		return err
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	s.mu.Lock()
	// Only sendFrame appends, under sendMu, so req is last unless the first
	// response has already cleared the list.
	if n := len(s.unanswered); n > 0 && s.unanswered[n-1] == req {
		s.unanswered = s.unanswered[:n-1]
	}
	s.mu.Unlock()
	return err
}
