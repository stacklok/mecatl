package identityharness

import "net/http"

// RevocationEndpoint is AS-1's RFC 7009 revocation endpoint.
func (a *AS1) RevocationEndpoint() string { return a.Issuer() + "/revoke" }

// revoke is an RFC 7009 revocation endpoint for session refresh tokens.
//
// Guess: only the authenticated broker may call it. Revoking the session's
// current refresh token closes the session (design step (3): revoking RT-A42
// closes A42); revoking a replaced token or any other value changes nothing
// and, as RFC 7009 §2.2 requires for an invalid token, still answers 200.
func (a *AS1) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	if _, confidential, ok := a.authenticateClient(r); !ok || !confidential {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	a.mu.Lock()
	if rec, ok := a.sessionTokens[token]; ok && rec.clientID == BrokerClientID {
		if s := a.sessionLocked(rec.session); s.current == token {
			s.closed = true
		}
	}
	a.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
