package client

import (
	"errors"
	"io"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// flowControlledSender's Send blocks, as gRPC flow control does while the
// server waits for the client to read, until the reader has finished its
// first-response bookkeeping. It gives up after a timeout so a regression fails
// instead of hanging.
type flowControlledSender struct{ entered, answered chan struct{} }

func (f flowControlledSender) Send(*mecatlv1.ConverseRequest) error {
	close(f.entered)
	select {
	case <-f.answered:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("send still blocked: the reader never finished its first response")
	}
}

func TestBlockedSendDoesNotStopTheReader(t *testing.T) {
	answered := make(chan struct{})
	sender := flowControlledSender{entered: make(chan struct{}), answered: answered}
	s := &reopenableStream[mecatlv1.ConverseRequest, mecatlv1.ConverseResponse]{
		recv:       newFakeStream(resp(&mecatlv1.Event{Type: "message.delta", Text: "hi"})),
		send:       sender,
		onAnswered: func() { close(answered) },
	}
	s.armReopen(func() (recvSide[mecatlv1.ConverseResponse], sendSide[mecatlv1.ConverseRequest], error) {
		return nil, nil, errors.New("unexpected reopen")
	})

	sent := make(chan error, 1)
	go func() { sent <- s.sendFrame(&mecatlv1.ConverseRequest{}) }()
	<-sender.entered
	if _, err := s.receive(); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("sendFrame: %v", err)
	}
}

// scriptedSender returns its errors in order, then nil.
type scriptedSender struct{ errs []error }

func (s *scriptedSender) Send(*mecatlv1.ConverseRequest) error {
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

// TestFailedSendIsNotReplayed: a frame whose Send failed is reported to the
// caller and not replayed on the reopened stream; one that met io.EOF (the
// stream already ended) is.
func TestFailedSendIsNotReplayed(t *testing.T) {
	rejected := newFakeStream()
	rejected.endErr = bearerRejectedStatus()
	replacement := newFakeStream(resp(&mecatlv1.Event{Type: "message.delta", Text: "hi"}))
	sendErr := errors.New("send failed")
	s := newAuthenticatedStream(rejected, &scriptedSender{errs: []error{sendErr, io.EOF}}, true)
	s.enableReopen(func() (Recver, Sender, error) { return replacement, replacement, nil })

	if err := s.SendPrompt("s", "lost", nil); !errors.Is(err, sendErr) {
		t.Fatalf("SendPrompt with a failed Send = %v, want %v", err, sendErr)
	}
	if err := s.SendPrompt("s", "kept", nil); err != nil {
		t.Fatalf("SendPrompt on an ended stream: %v", err)
	}
	if _, err := s.receive(); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if sent := replacement.sentFrames(); len(sent) != 1 || sent[0].GetPrompt().GetText() != "kept" {
		t.Fatalf("replacement received %d frames, want only the one that met io.EOF", len(sent))
	}
}
