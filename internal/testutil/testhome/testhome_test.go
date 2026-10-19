package testhome

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
)

func TestRunIsolatesAllXDGRootsAndRestoresProcessState(t *testing.T) {
	roots := map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "config",
		"XDG_DATA_HOME":   "data",
		"XDG_STATE_HOME":  "state",
		"XDG_CACHE_HOME":  "cache",
		"XDG_RUNTIME_DIR": "runtime",
	}
	original := make(map[string]struct {
		value string
		set   bool
	}, len(roots))
	poison := make(map[string]string, len(roots))
	marker, markerSet := os.LookupEnv("MECATL_TEST_TEMP_LEASE")
	t.Cleanup(func() {
		for name, value := range original {
			if value.set {
				_ = os.Setenv(name, value.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
		if markerSet {
			_ = os.Setenv("MECATL_TEST_TEMP_LEASE", marker)
		} else {
			_ = os.Unsetenv("MECATL_TEST_TEMP_LEASE")
		}
		xdg.Reload()
	})
	for name := range roots {
		value, set := os.LookupEnv(name)
		original[name] = struct {
			value string
			set   bool
		}{value, set}
		poison[name] = filepath.Join(t.TempDir(), "poison", roots[name])
		if err := os.Setenv(name, poison[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Unsetenv("MECATL_TEST_TEMP_LEASE"); err != nil {
		t.Fatal(err)
	}
	xdg.Reload()

	var root string
	if exit := Run("isolated", func() int {
		root = filepath.Dir(os.Getenv("HOME"))
		for name, leaf := range roots {
			dir := os.Getenv(name)
			if want := filepath.Join(root, leaf); dir != want {
				t.Errorf("%s = %q, want %q", name, dir, want)
			}
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Errorf("%s directory = %v, %v; want private directory", name, info, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "isolated"), []byte("ok"), 0o600); err != nil {
				t.Errorf("write %s: %v", name, err)
			}
		}
		if xdg.DataHome != filepath.Join(root, "data") {
			t.Errorf("xdg.DataHome = %q, want %q", xdg.DataHome, filepath.Join(root, "data"))
		}
		return 0
	}); exit != 0 {
		t.Fatalf("Run exit = %d", exit)
	}

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temporary root remains at %q: %v", root, err)
	}
	for name, path := range poison {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("poison %s path was used: %q, %v", name, path, err)
		}
		if got := os.Getenv(name); got != path {
			t.Errorf("%s after Run = %q, want restored %q", name, got, path)
		}
	}
	if xdg.DataHome != poison["XDG_DATA_HOME"] {
		t.Errorf("xdg.DataHome after Run = %q, want restored %q", xdg.DataHome, poison["XDG_DATA_HOME"])
	}
}

func TestRunUsesActiveManagedCmdAndJobLeaseRuntime(t *testing.T) {
	ns, err := managedtemp.Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "test-home-managed-lease", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	if err := os.Remove(filepath.Join(ns.Path(), "gc.lock")); err != nil {
		t.Fatalf("remove test namespace gc lock: %v", err)
	}

	for _, kind := range []string{"cmd", "job"} {
		t.Run(kind, func(t *testing.T) {
			t.Cleanup(xdg.Reload)
			lease, err := workspace.Allocate(kind)
			if err != nil {
				t.Fatal(err)
			}
			ambient := map[string]string{
				"HOME":            filepath.Join(t.TempDir(), "ambient-home"),
				"XDG_CONFIG_HOME": filepath.Join(t.TempDir(), "ambient-config"),
				"XDG_DATA_HOME":   filepath.Join(t.TempDir(), "ambient-data"),
				"XDG_STATE_HOME":  filepath.Join(t.TempDir(), "ambient-state"),
				"XDG_CACHE_HOME":  filepath.Join(t.TempDir(), "ambient-cache"),
				"XDG_RUNTIME_DIR": filepath.Join(t.TempDir(), "ambient-runtime"),
			}
			t.Setenv("MECATL_TEST_TEMP_LEASE", lease.Path())
			for name, value := range ambient {
				t.Setenv(name, value)
			}
			xdg.Reload()

			var runtime string
			if exit := Run("managed", func() int {
				runtime = os.Getenv("XDG_RUNTIME_DIR")
				if want := lease.TempDir(); runtime != want {
					t.Errorf("XDG_RUNTIME_DIR = %q, want %q", runtime, want)
				}
				if got, want := os.Getenv("HOME"), filepath.Join(lease.Path(), "test-home", "home"); got != want {
					t.Errorf("HOME = %q, want isolated %q", got, want)
				}
				if err := os.WriteFile(filepath.Join(runtime, "test.sock"), nil, 0o600); err != nil {
					t.Errorf("write in managed runtime: %v", err)
					return 1
				}
				return 0
			}); exit != 0 {
				t.Fatalf("Run exit = %d", exit)
			}
			if _, err := os.Lstat(filepath.Join(ns.Path(), "gc.lock")); !os.IsNotExist(err) {
				t.Fatalf("testhome created gc.lock: %v", err)
			}
			if _, err := os.Stat(filepath.Join(runtime, "test.sock")); err != nil {
				t.Fatalf("runtime entry does not survive Run: %v", err)
			}
			for name, want := range ambient {
				if got := os.Getenv(name); got != want {
					t.Errorf("%s after Run = %q, want restored %q", name, got, want)
				}
			}
			if got := xdg.DataHome; got != ambient["XDG_DATA_HOME"] {
				t.Errorf("xdg.DataHome after Run = %q, want restored %q", got, ambient["XDG_DATA_HOME"])
			}
			if err := lease.Remove(); err != nil {
				t.Fatalf("remove lease: %v", err)
			}
			if _, err := os.Stat(lease.Path()); !os.IsNotExist(err) {
				t.Fatalf("removed lease remains at %q: %v", lease.Path(), err)
			}
		})
	}
}
