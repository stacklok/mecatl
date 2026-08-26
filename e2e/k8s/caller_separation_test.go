//go:build kind_e2e

// These journeys are the derived proof for issue #368 (caller separation,
// ADR-0212): they were written AFTER hand-driving the equivalent curl journeys
// against a real kind cluster (real Dex tokens for alice@example.com/bob@example.com,
// real mecak8s pods built from this commit) and observing the actual responses —
// the same discipline caller_identity_test.go's own header describes. Two real
// gaps were found and fixed this way before this file existed: cmd/mecak8s never
// wired app.Config.OwnershipEnforced at all (fixed in cmd/mecak8s/flags.go), and
// CreateSchedule's collision guard leaked cross-tenant existence via a
// distinguishing error before the schedule store key was owner-namespaced (fixed
// in internal/adapter/server/schedule_manager.go). Both fixes are exercised here.
//
// Session-level isolation (list scoping, foreign-prompt refusal, actor-vs-owner
// on the still-allowed path) is pinned by the FLIPPED Story 3/4 specs in
// caller_identity_test.go, not duplicated here — this file covers the surfaces
// those stories don't: schedules (the owner-key fix) and session fork/carryover
// (AC1.6).
package k8s_e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// --- schedule request bodies + handlers --------------------------------------

// scheduleBody is the minimal valid create/update body this suite needs: a
// mutating cron schedule. Under this deployment's server-assigned workspace
// authority (ADR 0237) the request carries an EMPTY workspace — the server
// assigns its configured root at fire time; a non-mutating schedule still
// requires plan mode.
func scheduleBody(name string) []byte {
	body, _ := json.Marshal(map[string]any{
		"name":      name,
		"prompt":    "noop",
		"trigger":   map[string]any{"cron": "0 0 * * *"},
		"mutating":  true,
		"workspace": "",
	})
	return body
}

// createScheduleAs POSTs a schedule as bearer and returns the status code plus
// the raw response body (the caller decides what to assert from it — a create
// can legitimately fail with a validation error unrelated to ownership).
func createScheduleAs(ctx context.Context, addr, bearer, name string) (int, []byte) {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/v1/schedules", addr), bytes.NewReader(scheduleBody(name)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST /v1/schedules")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, raw
}

// getScheduleAs GETs a schedule by name as bearer.
func getScheduleAs(ctx context.Context, addr, bearer, name string) (int, []byte) {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s/v1/schedules/%s", addr, name), nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "GET /v1/schedules/%s", name)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, raw
}

// deleteScheduleAs DELETEs a schedule by name as bearer.
func deleteScheduleAs(ctx context.Context, addr, bearer, name string) int {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("http://%s/v1/schedules/%s", addr, name), nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "DELETE /v1/schedules/%s", name)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// schedulePrompt reads the spec.prompt field off a getSchedule/createSchedule
// response body, so a test can prove WHICH owner's schedule survives a
// same-name collision without assuming JSON field order.
func schedulePrompt(raw []byte) string {
	ginkgo.GinkgoHelper()
	var out struct {
		Schedule struct {
			Spec struct {
				Prompt string `json:"prompt"`
			} `json:"spec"`
		} `json:"schedule"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &out)).To(gomega.Succeed(), "unmarshal %s", raw)
	return out.Schedule.Spec.Prompt
}

// scheduleBodyWithPrompt is scheduleBody with a caller-chosen prompt, so two
// same-named schedules from different owners can be told apart by content.
func scheduleBodyWithPrompt(name, prompt string) []byte {
	body, _ := json.Marshal(map[string]any{
		"name":      name,
		"prompt":    prompt,
		"trigger":   map[string]any{"cron": "0 0 * * *"},
		"mutating":  true,
		"workspace": "",
	})
	return body
}

func createScheduleWithPromptAs(ctx context.Context, addr, bearer, name, prompt string) (int, []byte) {
	ginkgo.GinkgoHelper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/v1/schedules", addr), bytes.NewReader(scheduleBodyWithPrompt(name, prompt)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST /v1/schedules")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, raw
}

// forkSessionAs attempts to create a session carrying source_session_id, as a
// real fork/carryover request would.
func forkSessionAs(ctx context.Context, addr, bearer, sourceID string) (int, []byte) {
	ginkgo.GinkgoHelper()
	body, _ := json.Marshal(map[string]any{
		"workspace":         "",
		"mode":              "default",
		"source_session_id": sourceID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/v1/sessions", addr), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST /v1/sessions (fork)")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, raw
}

// --- the stories -------------------------------------------------------------

var _ = ginkgo.Describe("caller separation (issue #368)", ginkgo.Serial, ginkgo.Ordered, func() {
	ginkgo.Context("schedule ownership", ginkgo.Ordered, func() {
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

		// AC6.1/AC6.3 — two owners may share a schedule name; a same-name create
		// from a different owner neither leaks existence nor collides.
		ginkgo.It("lets two owners use the identical schedule name without collision or leak", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			const name = "daily-report"

			st, aliceRaw := createScheduleWithPromptAs(ctx, addr, alice, name, "alice's report")
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated),
				"alice's create failed: %s", aliceRaw)

			// Bob's create of the SAME name must succeed — not be rejected as a
			// collision (that would be the pre-fix existence leak) and not silently
			// overwrite Alice's record.
			st, bobRaw := createScheduleWithPromptAs(ctx, addr, bob, name, "bob's report")
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated),
				"bob's create of the same name as a different owner was refused (existence "+
					"leak / missing owner-key namespacing): %s", bobRaw)

			// Each owner reads back their OWN content under the shared name — proof
			// the physical keys are genuinely distinct, not a coincidental double-write.
			st, raw := getScheduleAs(ctx, addr, alice, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusOK))
			gomega.Expect(schedulePrompt(raw)).To(gomega.Equal("alice's report"),
				"alice's schedule was clobbered by bob's same-named create")

			st, raw = getScheduleAs(ctx, addr, bob, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusOK))
			gomega.Expect(schedulePrompt(raw)).To(gomega.Equal("bob's report"),
				"bob's schedule did not persist independently of alice's")

			// Cleanup: each owner deletes their own.
			gomega.Expect(deleteScheduleAs(ctx, addr, alice, name)).To(gomega.Equal(http.StatusNoContent))
			gomega.Expect(deleteScheduleAs(ctx, addr, bob, name)).To(gomega.Equal(http.StatusNoContent))
		})

		// AC6.2 — a same-OWNER reuse of a name is still rejected, unchanged from
		// before the owner-key fix.
		ginkgo.It("still rejects a same-owner name reuse", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			const name = "weekly-digest"
			st, raw := createScheduleAs(ctx, addr, alice, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated), "%s", raw)

			st, raw = createScheduleAs(ctx, addr, alice, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusBadRequest),
				"alice's own same-name re-create was not rejected as a collision: %s", raw)

			gomega.Expect(deleteScheduleAs(ctx, addr, alice, name)).To(gomega.Equal(http.StatusNoContent))
		})

		// AC2.4 — a direct load/delete of another caller's schedule is
		// indistinguishable from a missing one, and cannot mutate it.
		ginkgo.It("hides and protects a foreign caller's schedule", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			const name = "alice-only"
			st, raw := createScheduleWithPromptAs(ctx, addr, alice, name, "untouched")
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated), "%s", raw)

			st, _ = getScheduleAs(ctx, addr, bob, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusNotFound),
				"bob could read alice's schedule by name")

			st = deleteScheduleAs(ctx, addr, bob, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusNotFound),
				"bob's delete of alice's schedule was not refused as absent")

			// Alice's schedule is untouched by bob's attempts.
			st, raw = getScheduleAs(ctx, addr, alice, name)
			gomega.Expect(st).To(gomega.Equal(http.StatusOK))
			gomega.Expect(schedulePrompt(raw)).To(gomega.Equal("untouched"),
				"bob's refused delete attempt still mutated alice's schedule")

			gomega.Expect(deleteScheduleAs(ctx, addr, alice, name)).To(gomega.Equal(http.StatusNoContent))
		})
	})

	ginkgo.Context("session fork and carryover", ginkgo.Ordered, func() {
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

		// AC1.6 — Bob cannot fork Alice's session; the source reference is
		// absence-shaped, creates no destination, and leaves Alice's session
		// unchanged. Alice's own fork/carryover remains available.
		ginkgo.It("refuses a foreign fork source and leaves the source untouched", func() {
			ctx := ginkgoSuiteCtx()
			addr, stop := portForward(agentPods[0])
			defer stop()

			st, aliceSess := createSessionAs(ctx, addr, alice)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated))

			beforeSub, beforeName := sessionOwner(ctx, addr, alice, aliceSess)

			st, raw := forkSessionAs(ctx, addr, bob, aliceSess)
			gomega.Expect(st).To(gomega.Equal(http.StatusNotFound),
				"bob forking alice's session as source was not refused as absent: %s", raw)

			// The source is unchanged — no destination was created off it, and its
			// own owner/state did not move.
			afterSub, afterName := sessionOwner(ctx, addr, alice, aliceSess)
			gomega.Expect(afterName).To(gomega.Equal(beforeName))
			gomega.Expect(afterSub).To(gomega.Equal(beforeSub))

			// Alice's own carryover from her own session succeeds.
			st, raw = forkSessionAs(ctx, addr, alice, aliceSess)
			gomega.Expect(st).To(gomega.Equal(http.StatusCreated),
				"alice's own fork/carryover from her own session was refused: %s", raw)
		})
	})
})
