package executioncontroller

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestCommandInputClosesIndependentlyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, closeInput := commandInput(ctx, []byte(`{"operation":"command.start"}`))
	defer closeInput()

	line := make([]byte, len(`{"operation":"command.start"}`)+1)
	if _, err := io.ReadFull(reader, line); err != nil {
		t.Fatal(err)
	}
	cancel()
	readDone := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := reader.Read(one[:])
		readDone <- err
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("command control pipe remained open after cancellation")
		}
	case <-deadline.C:
		t.Fatal("command control pipe did not close after cancellation")
	}
}
