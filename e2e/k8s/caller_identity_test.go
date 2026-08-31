//go:build kind_e2e

// These journeys exercise the production toolhive-core/authn validator through
// the canonical GOWORK=off kind build.
//
// Every assertion here encodes a fact OBSERVED by hand in a live cluster first
// (the probe transcript). The earlier drafts of this file asserted assumptions and
// each cost a five-minute suite run to disprove — the owner was assumed absent
// from the API, an IdP outage was assumed to 503, and the durable event was
// assumed to be a flat snake_cased record. All three were wrong.

package k8s_e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// --- bearer-carrying HTTP helpers --------------------------------------------
//
// The suite's existing helpers send no Authorization header, which is right for
// the unauthenticated base. Caller identity is inherently MULTI-caller, so these
// take the bearer per call rather than reading one shared credential.

func createSessionAs(ctx context.Context, addr, bearer string) (int, string) {
	ginkgo.GinkgoHelper()
	body, _ := json.Marshal(map[string]any{"workspace": "", "mode": "default"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/v1/sessions", addr), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST /v1/sessions")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusCreated {
		return resp.StatusCode, ""
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &out)).To(gomega.Succeed(), "unmarshal %s", raw)
	return resp.StatusCode, out.SessionID
}

// promptAs drives a prompt through the authenticated edge, draining the SSE body
// so the run reaches terminal and its events are appended before we assert.
func promptAs(ctx context.Context, addr, sessionID, bearer, text string) int {
	ginkgo.GinkgoHelper()
	body, _ := json.Marshal(map[string]any{"text": text})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST prompt")
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode
}

// sessionOwner reads the owner off the LIST row over plain HTTP.
//
// `GET /v1/sessions` carries owner{issuer,subject,grant_type,name} — verified in
// the cluster. An earlier draft read Redis instead, on a mistaken belief that
// ownership was gRPC-only; asserting the API is both correct and closer to what
// an operator actually sees. Returns ("", "") when the session has no owner.
func sessionOwner(ctx context.Context, addr, bearer, sessionID string) (subject, name string) {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s/v1/sessions", addr), nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "GET /v1/sessions")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusOK),
		"list sessions: %d %s", resp.StatusCode, raw)

	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Owner     *struct {
				Subject string `json:"subject"`
				Name    string `json:"name"`
			} `json:"owner"`
		} `json:"sessions"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &out)).To(gomega.Succeed(), "unmarshal %s", raw)
	for _, s := range out.Sessions {
		if s.SessionID != sessionID {
			continue
		}
		if s.Owner == nil {
			return "", ""
		}
		return s.Owner.Subject, s.Owner.Name
	}
	return "", ""
}

// eventActors returns the actor subjects on a session's DURABLE event log.
//
// Redis is the only observation point: Event.Actor is log-only by design (toProto
// omits it), so no API returns it. Three shapes must be handled, and the first two
// were got wrong by an earlier draft, which could therefore only ever time out:
//   - the record is an ENVELOPE, {"v":"redisstore-eventlog/1","ev":<event>}
//   - session.Event carries NO json tags, so keys are GO-CASED ("Actor"/"Subject")
//   - the log is a STREAM, not a LIST (ADR 0250), so the read is XRANGE and
//     redis-cli's raw (non-TTY) output FLATTENS each entry to three lines:
//     the entry id, the field name, then the value. LRANGE here returns
//     WRONGTYPE, whose error text is not JSON.
//
// Every parse failure is asserted rather than skipped. A fail-silent parser
// guarding a security property is worse than no test: the moment this assertion is
// weakened to a negative, a silent skip makes it pass forever on an empty list.
// That discipline is why the LIST-to-Stream move surfaced here as an immediate,
// named parse failure instead of a 90-second Eventually timeout.
func eventActors(sessionID string) []string {
	ginkgo.GinkgoHelper()
	raw := runCmdQuiet("kubectl", "exec", "-n", k8sNamespace, "redis-0", "--",
		"redis-cli", "XRANGE", "mecatl:events:"+sessionID, "-", "+")
	// A missing key (nothing appended yet) is an empty result, so the caller's
	// Eventually keeps polling rather than failing — absence is data here too.
	lines := splitNonEmptyLines(raw)
	gomega.ExpectWithOffset(1, len(lines)%3).To(gomega.Equal(0),
		"XRANGE returned %d lines, not a whole number of 3-line entries: %.200s", len(lines), raw)
	var subs []string
	for i := 0; i+2 < len(lines); i += 3 {
		// Assert the entry shape rather than scanning for JSON-looking lines: a
		// stride that silently resynchronised would hide exactly the datatype
		// drift this parse is here to catch.
		gomega.ExpectWithOffset(1, lines[i+1]).To(gomega.Equal("r"),
			"stream entry %q field is %q, want \"r\" (redisstore.recordField) — entry shape changed: %.200s",
			lines[i], lines[i+1], raw)
		line := lines[i+2]
		var rec struct {
			V  string          `json:"v"`
			Ev json.RawMessage `json:"ev"`
		}
		gomega.ExpectWithOffset(1, json.Unmarshal([]byte(line), &rec)).To(gomega.Succeed(),
			"event-log record is not the expected envelope: %.200s", line)
		if rec.V == "redisstore-eventlog-gap/1" {
			// A gap marker (ADR 0250) legitimately carries no event and so no
			// actor. Nothing calls AppendGap in production yet, but skipping it
			// here keeps this helper from turning the FIRST failed append on a
			// healthy cluster into a confusing failure of an identity assertion.
			continue
		}
		gomega.ExpectWithOffset(1, rec.Ev).NotTo(gomega.BeEmpty(),
			"envelope carried no `ev` payload: %.200s", line)

		var ev struct {
			Type  string `json:"Type"`
			Actor *struct {
				Subject string `json:"Subject"`
				Name    string `json:"Name"`
			} `json:"Actor"`
		}
		gomega.ExpectWithOffset(1, json.Unmarshal(rec.Ev, &ev)).To(gomega.Succeed(),
			"event payload did not parse: %.200s", rec.Ev)
		if ev.Actor != nil {
			subs = append(subs, ev.Actor.Subject)
		}
	}
	return subs
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range bytes.Split([]byte(s), []byte("\n")) {
		if t := bytes.TrimSpace(l); len(t) > 0 {
			out = append(out, string(t))
		}
	}
	return out
}

// listSessionIDs returns every session id the given caller can see.
//
// Deliberately separate from sessionOwner: Expect() on a multi-value call treats
// the extra returns as "must be zero", which is how the first version of this
// spec failed with `Unexpected non-nil/non-zero argument at index 1: "bob"`.
func listSessionIDs(ctx context.Context, addr, bearer string) []string {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s/v1/sessions", addr), nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "GET /v1/sessions")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusOK),
		"list sessions: %d %s", resp.StatusCode, raw)
	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &out)).To(gomega.Succeed(), "unmarshal %s", raw)
	ids := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		ids = append(ids, s.SessionID)
	}
	return ids
}

// --- the stories -------------------------------------------------------------

var _ = ginkgo.Describe("caller identity, from the caller's and operator's view", ginkgo.Serial, ginkgo.Ordered, func() {
	ginkgo.Context("bounded JWKS staleness", ginkgo.Ordered, func() {
		const maxStaleness = 2 * time.Second

		var (
			dex           *idp
			alice         string
			proxyDeployed bool
		)

		ginkgo.BeforeAll(func() {
			ctx := ginkgoSuiteCtx()
			dex = deployDex(ctx)
			deployJWKSProxy(ctx)
			proxyDeployed = true
			patchAgentToOIDCWithCachePolicy(ctx, jwksProxyURI, maxStaleness)
			alice = dex.token(ctx, aliceEmail)
		})

		ginkgo.AfterAll(func() {
			ctx := ginkgoSuiteCtx()
			// Restore reachability before removing the OIDC fixture so no independent
			// later journey inherits an infrastructure outage.
			if proxyDeployed {
				setJWKSProxyReplicas(ctx, 1)
			}
			restoreAgentFromOIDC(ctx)
			if dex != nil {
				dex.stop()
			}
		})

		ginkgo.It("fails stale cached keys with 503 and recovers when the issuer returns", func() {
			addr, stop := portForward(agentPods[0])
			defer stop()

			warmCtx, warmCancel := shortCtx(30 * time.Second)
			status, _ := createSessionAs(warmCtx, addr, alice)
			warmCancel()
			gomega.Expect(status).To(gomega.Equal(http.StatusCreated),
				"the Dex token did not validate while discovery and JWKS were available")

			setJWKSProxyReplicas(ginkgoSuiteCtx(), 0)
			time.Sleep(maxStaleness)

			gomega.Eventually(func() int {
				requestCtx, cancel := shortCtx(25 * time.Second)
				defer cancel()
				status, _ := createSessionAs(requestCtx, addr, alice)
				return status
			}, 60*time.Second, time.Second).Should(gomega.Equal(http.StatusServiceUnavailable),
				"after cached JWKS exceeds %s the same token must yield 503, never 401 or success",
				maxStaleness)

			setJWKSProxyReplicas(ginkgoSuiteCtx(), 1)
			gomega.Eventually(func() int {
				requestCtx, cancel := shortCtx(25 * time.Second)
				defer cancel()
				status, _ := createSessionAs(requestCtx, addr, alice)
				return status
			}, 60*time.Second, time.Second).Should(gomega.Equal(http.StatusCreated),
				"the validator did not recover after Dex/JWKS became reachable again")
		})
	})

	ginkgo.Context("authenticated caller journey", ginkgo.Ordered, func() {
		var (
			dex   *idp
			alice string
		)

		ginkgo.BeforeAll(func() {
			ctx := ginkgoSuiteCtx()
			dex = deployDex(ctx)
			// Enabling identity is itself Story 1: a validator that cannot be built is
			// a FATAL startup error, so a completed rollout IS the assertion that the
			// composed binary — flags, discovery, validator, middleware — works. This
			// is the layer that was green in every unit test while both mains shipped a
			// nil validator and no real deployment could start.
			patchAgentToOIDC(ctx)
			alice = dex.token(ctx, aliceEmail)
		})

		ginkgo.AfterAll(func() {
			restoreAgentFromOIDC(ginkgoSuiteCtx())
			if dex != nil {
				dex.stop()
			}
		})

		// Story 2 — "I authenticate, and the work I do is mine."
		ginkgo.It("attributes an authenticated run to the caller who made it", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			st, sess := createSessionAs(ctx, addr, alice)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated),
				"a real IdP-issued token was refused — identity is not working in the cluster")

			// The RUN is the point. Session creation alone never exercises the prompt
			// path through the authenticated edge, and three scenarios of coverage
			// existed before any authenticated run had been driven.
			gomega.Expect(promptAs(ctx, addr, sess, alice, "hello from alice")).
				To(gomega.Equal(http.StatusOK), "an authenticated run was refused")

			sub, name := sessionOwner(ctx, addr, alice, sess)
			gomega.Expect(sub).NotTo(gomega.BeEmpty(), "the session recorded no owner")
			gomega.Expect(name).To(gomega.Equal("alice"),
				"the owner's display name did not come from the token's name claim")
		})

		// Story 5 — "A caller without a valid token gets nothing."
		ginkgo.It("refuses a forged signature and a missing credential", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			// A real `kid` signed with a key the IdP never published: the validator finds
			// the named key and must reject on the SIGNATURE. Without this, an edge that
			// parsed a JWT without verifying it would pass every other assertion here.
			forged := dex.forgedToken(ctx, "alice-impostor")
			st, _ := createSessionAs(ctx, addr, forged)
			gomega.Expect(st).To(gomega.Equal(http.StatusUnauthorized),
				"a token signed by an unpublished key was ACCEPTED — signature verification is not happening")

			st, _ = createSessionAs(ctx, addr, "")
			gomega.Expect(st).To(gomega.Equal(http.StatusUnauthorized),
				"an unauthenticated request was accepted while identity is on")
		})

		// Story 5a — the same real-Dex credentials work through the gRPC edge used by
		// remote mecatui clients. The mock provider makes CreateSession sufficient: it
		// proves metadata extraction, interceptor validation, and principal admission
		// without requiring a live LLM turn.
		ginkgo.It("authenticates gRPC requests with Dex bearer metadata", func() {
			addr, stop := portForwardGRPC(agentPods[0])
			defer stop()

			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "dial gRPC port-forward")
			defer func() { _ = conn.Close() }()
			client := mecatlv1.NewHarnessServiceClient(conn)

			request := func(ctx context.Context) error {
				_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
				return err
			}
			ctx, cancel := shortCtx(30 * time.Second)
			defer cancel()
			authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+alice)
			gomega.Expect(request(authCtx)).To(gomega.Succeed(),
				"a real Dex token was refused by the gRPC interceptor")

			missingCtx, missingCancel := shortCtx(30 * time.Second)
			defer missingCancel()
			gomega.Expect(status.Code(request(missingCtx))).To(gomega.Equal(codes.Unauthenticated),
				"gRPC accepted a request without a bearer while identity is on")

			forgedCtx, forgedCancel := shortCtx(30 * time.Second)
			defer forgedCancel()
			forgedCtx = metadata.AppendToOutgoingContext(forgedCtx, "authorization", "Bearer "+dex.forgedToken(forgedCtx, "alice-impostor"))
			gomega.Expect(status.Code(request(forgedCtx))).To(gomega.Equal(codes.Unauthenticated),
				"gRPC accepted a token signed by an unpublished key")
		})
	})

	ginkgo.Context("shared deployment attribution and durability journey", ginkgo.Ordered, func() {
		var (
			dex        *idp
			alice, bob string
		)

		ginkgo.BeforeAll(func() {
			ctx := ginkgoSuiteCtx()
			dex = deployDex(ctx)
			patchAgentToOIDC(ctx)
			alice = dex.token(ctx, aliceEmail)
			bob = dex.token(ctx, bobEmail)
		})

		ginkgo.AfterAll(func() {
			restoreAgentFromOIDC(ginkgoSuiteCtx())
			if dex != nil {
				dex.stop()
			}
		})

		// Story 3 — "I can see who owns what, and only what's mine."
		//
		// FLIPPED for issue #368 (caller separation): this phase used to ship
		// attribution with no isolation, and this spec asserted exactly that
		// (Bob's list containing Alice's session) so the absence of scoping could
		// not be mistaken for a bug. #368 landed application-wide ownership
		// enforcement (ADR-0212) — a caller's list now contains only that
		// caller's own rows (AC2.3). Do not delete this spec; it is the
		// regression pin for that scoping.
		ginkgo.It("shows the owner on the list row over plain HTTP, scoped to the caller's own sessions", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			st, bobSess := createSessionAs(ctx, addr, bob)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated))

			bobSub, name := sessionOwner(ctx, addr, bob, bobSess)
			gomega.Expect(name).To(gomega.Equal("bob"))

			_, aliceSess := createSessionAs(ctx, addr, alice)
			aliceSub, aliceName := sessionOwner(ctx, addr, alice, aliceSess)
			gomega.Expect(aliceName).To(gomega.Equal("alice"))
			gomega.Expect(bobSub).NotTo(gomega.Equal(aliceSub),
				"two different callers produced the same subject — identity is not per-caller")

			gomega.Expect(listSessionIDs(ctx, addr, bob)).To(gomega.ContainElement(bobSess),
				"bob's own session list omitted his own session")
			gomega.Expect(listSessionIDs(ctx, addr, bob)).NotTo(gomega.ContainElement(aliceSess),
				"bob's session list included alice's session — issue #368 ownership enforcement is not scoping lists")
			gomega.Expect(listSessionIDs(ctx, addr, alice)).To(gomega.ContainElement(aliceSess),
				"alice's own session list omitted her own session")
		})

		// Story 4 — "Nobody but me can act on my session, and the audit trail
		// still names the actor correctly on the operations I do allow."
		//
		// FLIPPED for issue #368: this phase used to permit Bob to prompt Alice's
		// session (recording him as actor, her as owner — attribution without
		// isolation) and this spec asserted exactly that. #368 landed enforcement:
		// a foreign prompt is now refused, absence-shaped (AC3.1). The
		// actor-vs-owner distinction this spec also pins (ADR-0204 decision 7)
		// remains real and is now re-asserted on ALICE's own action instead, so
		// this spec keeps covering both properties rather than losing one.
		ginkgo.It("refuses a foreign caller and records the acting caller as the actor on the owner's own run", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			st, aliceSess := createSessionAs(ctx, addr, alice)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated))
			aliceSubject, ownerName := sessionOwner(ctx, addr, alice, aliceSess)
			gomega.Expect(ownerName).To(gomega.Equal("alice"))
			gomega.Expect(aliceSubject).NotTo(gomega.BeEmpty())

			// Bob acting on Alice's session is now refused, absence-shaped (a 404,
			// not a distinguishing 403 — AC2.4/AC3.1's "indistinguishable from
			// missing" contract).
			bobStatus := promptAs(ctx, addr, aliceSess, bob, "bob acting on alice's session")
			gomega.Expect(bobStatus).To(gomega.Equal(http.StatusNotFound),
				"bob was NOT refused on alice's session — issue #368 ownership enforcement regressed")

			ownerSub, ownerName := sessionOwner(ctx, addr, alice, aliceSess)
			gomega.Expect(ownerName).To(gomega.Equal("alice"),
				"a refused foreign prompt still changed the owner — ownership laundering")
			gomega.Expect(ownerSub).To(gomega.Equal(aliceSubject))

			// Alice's OWN prompt still succeeds and is attributed to her — the
			// owner/actor distinction (ADR-0204 decision 7) still holds on the
			// path that's still allowed.
			gomega.Expect(promptAs(ctx, addr, aliceSess, alice, "alice acting on her own session")).
				To(gomega.Equal(http.StatusOK), "alice was refused on her own session")
			gomega.Eventually(func() []string {
				return eventActors(aliceSess)
			}, 90*time.Second, 3*time.Second).Should(gomega.ContainElement(aliceSubject),
				"no durable event recorded alice as actor on her own run")
		})

		// Story 6 — "My session is still mine after the pod that took it dies."
		//
		// This is the ONLY story here that genuinely needs a cluster. Every other
		// assertion could in principle be made against one in-process server; this one
		// asks whether ownership is DURABLE STATE (in Redis, readable by any replica) or
		// merely in-process bookkeeping that happens to look right while one pod lives.
		// It was written, passed, and then LOST when this file was reorganised around
		// user stories — restored deliberately, and placed last because it destroys a
		// pod and reshuffles the roster the earlier specs port-forward to.
		// ponytail: no SpecTimeout — that decorator requires a SpecContext-taking body,
		// and every slow step here is already bounded by its own shortCtx.
		ginkgo.It("keeps the owner after the pod that recorded it is replaced",
			func() {
				refreshPods()
				podA, podB := agentPods[0], agentPods[1]

				addrA, stopA := portForward(podA)
				defer stopA()
				addrB, stopB := portForward(podB)
				defer stopB()

				cCtx, cCancel := shortCtx(30 * time.Second)
				st, sess := createSessionAs(cCtx, addrA, alice)
				cCancel()
				gomega.Expect(st).To(gomega.Equal(http.StatusCreated))
				oCtx, oCancel := shortCtx(30 * time.Second)
				aliceSubject, ownerName := sessionOwner(oCtx, addrA, alice, sess)
				oCancel()
				gomega.Expect(ownerName).To(gomega.Equal("alice"))
				gomega.Expect(aliceSubject).NotTo(gomega.BeEmpty())

				// Drive a run so the session has durable events, not just a create.
				rCtx, rCancel := shortCtx(60 * time.Second)
				gomega.Expect(promptAs(rCtx, addrA, sess, alice, "alice before failover")).
					To(gomega.Equal(http.StatusOK))
				rCancel()

				// Snapshot the roster BEFORE deleting, so waitReplacementReady waits for
				// the genuinely-new pod rather than being satisfied by survivor pod-B —
				// the trap called out in failover_test.go.
				preDelete := podNames()
				kubectlDeletePod(podA, false)
				waitReplacementReady(preDelete)

				// Read the owner from a replica that never served the create. If ownership
				// lived in process memory this returns empty; the assertion is that a
				// DIFFERENT pod answers the same question identically.
				oCtx, oCancel = shortCtx(60 * time.Second)
				defer oCancel()
				sub, name := sessionOwner(oCtx, addrB, alice, sess)
				gomega.Expect(name).To(gomega.Equal("alice"),
					"the surviving replica lost the owner's name — ownership is not durable across pods")
				gomega.Expect(sub).To(gomega.Equal(aliceSubject),
					"the surviving replica reported a different subject for the same session")
			})
	})
})
