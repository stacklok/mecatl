package cliconfig

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
)

func TestNativeEndpointOpenStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want llmendpoint.Status
	}{
		"absent key material": {
			err:  errors.Join(llmendpoint.ErrNotEnrolled, credentialstore.ErrNotFound),
			want: llmendpoint.StatusNotEnrolled,
		},
		"storage fault": {
			err:  errors.Join(llmendpoint.ErrNotEnrolled, credentialstore.ErrUnavailable),
			want: llmendpoint.StatusStorageUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := nativeEndpointOpenStatus(tc.err); got != tc.want {
				t.Fatalf("nativeEndpointOpenStatus(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
