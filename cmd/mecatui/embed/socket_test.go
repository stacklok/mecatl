package embed

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixSocketListenerDarwinFallsBackFromLongRuntimeDir(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin sockaddr_un path-length regression")
	}

	longBase := t.TempDir()
	for unixSocketPathFits(filepath.Join(longBase, "mecatui-1234567890", socketName)) {
		longBase = filepath.Join(longBase, "long-runtime-segment")
	}
	if err := os.MkdirAll(longBase, 0o700); err != nil {
		t.Fatalf("MkdirAll(long runtime dir): %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", longBase)

	lis, dir, sock, err := newUnixSocketListener()
	if err != nil {
		t.Fatalf("newUnixSocketListener: %v", err)
	}
	defer func() {
		_ = lis.Close()
		_ = os.RemoveAll(dir)
	}()

	if !unixSocketPathFits(sock) {
		t.Fatalf("fallback socket path is still too long: %q (%d bytes)", sock, len(sock))
	}
	adminSock := filepath.Join(dir, adminSocketName)
	if !unixSocketPathFits(adminSock) {
		t.Fatalf("fallback admin socket path is still too long: %q (%d bytes)", adminSock, len(adminSock))
	}
	if strings.HasPrefix(dir, longBase+string(filepath.Separator)) {
		t.Fatalf("socket dir %q remained under overlong runtime dir %q", dir, longBase)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(socket dir): %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("socket dir mode = %04o, want 0700", got)
	}
}
