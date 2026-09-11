package server

import (
	"sync"
	"testing"
)

func TestAuthAcceptedObservationsOnce(t *testing.T) {
	observations := &authAcceptedObservations{}

	for _, tc := range []struct {
		name      string
		category  authObservationCategory
		transport authObservationTransport
		want      *sync.Once
	}{
		{
			name:      "static bearer over gRPC",
			category:  authCategoryStaticBearer,
			transport: authTransportGRPC,
			want:      &observations.staticGRPC,
		},
		{
			name:      "static bearer over HTTP",
			category:  authCategoryStaticBearer,
			transport: authTransportHTTP,
			want:      &observations.staticHTTP,
		},
		{
			name:      "validated identity over gRPC",
			category:  authCategoryValidatedIdentity,
			transport: authTransportGRPC,
			want:      &observations.identityGRPC,
		},
		{
			name:      "validated identity over HTTP",
			category:  authCategoryValidatedIdentity,
			transport: authTransportHTTP,
			want:      &observations.identityHTTP,
		},
		{
			name:      "unknown category",
			category:  authObservationCategory("unknown"),
			transport: authTransportGRPC,
		},
		{
			name:      "unknown transport",
			category:  authCategoryStaticBearer,
			transport: authObservationTransport("unknown"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := observations.once(tc.category, tc.transport); got != tc.want {
				t.Fatalf("once(%q, %q) = %p, want %p", tc.category, tc.transport, got, tc.want)
			}
		})
	}
}
