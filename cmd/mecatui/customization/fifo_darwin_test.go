package customization

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Darwin cannot poll FIFOs (golang/go#24164), so reads block with an external timeout.
func openFIFO(t *testing.T, path string, flags int) *os.File {
	t.Helper()
	fd, err := unix.Open(path, flags|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), path)
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readFIFO(t *testing.T, file *os.File, buf []byte) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := file.Read(buf)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out reading %s", file.Name())
		return 0, nil
	}
}
