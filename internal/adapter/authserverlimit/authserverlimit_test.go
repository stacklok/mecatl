package authserverlimit

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

func TestLimitAndCaps(t *testing.T) {
	for name, test := range map[string]struct {
		limiter    *rate.Limiter
		query      string
		body       string
		wantCode   int
		wantReach  bool
		wantCapped bool
	}{
		"within bounds":      {limiter: rate.NewLimiter(rate.Inf, 0), body: "a=b", wantCode: http.StatusOK, wantReach: true},
		"bucket exhausted":   {limiter: rate.NewLimiter(0, 0), wantCode: http.StatusTooManyRequests},
		"oversized query":    {limiter: rate.NewLimiter(rate.Inf, 0), query: strings.Repeat("s", MaxQueryBytes+1), wantCode: http.StatusRequestURITooLong},
		"query at the cap":   {limiter: rate.NewLimiter(rate.Inf, 0), query: strings.Repeat("s", MaxQueryBytes), wantCode: http.StatusOK, wantReach: true},
		"oversized body":     {limiter: rate.NewLimiter(rate.Inf, 0), body: strings.Repeat("a", MaxBodyBytes+1), wantCode: http.StatusOK, wantReach: true, wantCapped: true},
		"caps only, no rate": {query: strings.Repeat("s", MaxQueryBytes+1), wantCode: http.StatusRequestURITooLong},
	} {
		t.Run(name, func(t *testing.T) {
			var reached, capped bool
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				reached = true
				_, err := io.ReadAll(r.Body)
				var tooLarge *http.MaxBytesError
				capped = errors.As(err, &tooLarge)
			})
			handler := Caps(next)
			if test.limiter != nil {
				handler = Limit(next, test.limiter)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/oauth/token?"+test.query, strings.NewReader(test.body)))
			if recorder.Code != test.wantCode || reached != test.wantReach || capped != test.wantCapped {
				t.Fatalf("status %d, reached %v, body capped %v; want %d, %v, %v", recorder.Code, reached, capped, test.wantCode, test.wantReach, test.wantCapped)
			}
		})
	}
}
