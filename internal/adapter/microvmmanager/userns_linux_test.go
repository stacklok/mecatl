//go:build linux

package microvmmanager

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestLinuxUserNamespaceControls(t *testing.T) {
	tests := []struct {
		name, max, clone string
		probe            error
		want             string
	}{
		{name: "enabled", max: "1024\n", clone: "1\n"},
		{name: "disabled by quota control", max: "0\n", clone: "1\n", want: "max_user_namespaces=0"},
		{name: "disabled by clone control", max: "1024\n", clone: "0\n", want: "unprivileged_userns_clone=0"},
		{name: "quota exhausted", max: "1024\n", clone: "1\n", probe: errors.New("resource temporarily unavailable"), want: "exhausted its namespace quota"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				switch path {
				case "/proc/sys/user/max_user_namespaces":
					return []byte(test.max), nil
				case "/proc/sys/kernel/unprivileged_userns_clone":
					return []byte(test.clone), nil
				default:
					return nil, fs.ErrNotExist
				}
			}
			err := checkLinuxUserNamespaceControls(read, func() error { return test.probe })
			if test.want == "" {
				if err != nil {
					t.Fatalf("enabled controls failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
