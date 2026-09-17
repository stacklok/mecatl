package executioncontroller

import "testing"

func TestRPCLimiterBoundsGlobalPerClientAndIdentityMap(t *testing.T) {
	limiter, err := NewRPCLimiter(&SecurityManager{}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !limiter.acquire("client-a") || limiter.acquire("client-a") {
		t.Fatal("per-client bound was not enforced")
	}
	if !limiter.acquire("client-b") || limiter.acquire("client-c") {
		t.Fatal("global bound was not enforced")
	}
	limiter.release("client-a")
	limiter.release("client-b")
	if len(limiter.clients) != 0 || limiter.global != 0 {
		t.Fatalf("released limiter retained cardinality: global=%d clients=%v", limiter.global, limiter.clients)
	}
}

func TestRPCLimiterRejectsInvalidBounds(t *testing.T) {
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {1, 2}} {
		if _, err := NewRPCLimiter(&SecurityManager{}, limits[0], limits[1]); err == nil {
			t.Fatalf("accepted limits %v", limits)
		}
	}
}
