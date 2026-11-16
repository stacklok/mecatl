//go:build unix && !darwin

package customization

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openFIFO(t *testing.T, path string, flags int) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, flags|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readFIFO(t *testing.T, file *os.File, buf []byte) (int, error) {
	t.Helper()
	if err := file.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return file.Read(buf)
}
