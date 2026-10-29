package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Capture without replacing exact request validation or provider execution.
type budgetEvidenceReflector struct {
	*agent.EvidenceReflector
	input     learning.Input
	estimates int
}

func (r *budgetEvidenceReflector) RequestTokenEstimate(in learning.Input) (int, error) {
	r.estimates++
	return r.EvidenceReflector.RequestTokenEstimate(in)
}

func (r *budgetEvidenceReflector) Reflect(ctx context.Context, in learning.Input) (learning.Outcome, session.AuxiliaryUsage, error) {
	r.input = in
	return r.EvidenceReflector.Reflect(ctx, in)
}

func budgetTestObserver(t *testing.T, limits agent.ReflectionLimits, turn mockllm.Turn) (*reflectionObserver, *budgetEvidenceReflector, *int) {
	t.Helper()
	calls := new(int)
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { *calls++ })}, turn)
	evidenceReflector, err := agent.NewEvidenceReflector(provider, testProviderModel("budget-model"), nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	reflector := &budgetEvidenceReflector{EvidenceReflector: evidenceReflector}
	return &reflectionObserver{reflector: reflector, repository: memproposal.New(), operatorMemory: memmemory.New(), mode: learning.Off, policy: learning.AlwaysPolicy{}}, reflector, calls
}

func budgetTrajectory(escaped bool) learning.Trajectory {
	text := strings.Repeat("x", 900)
	if escaped {
		text = strings.Repeat("<&>\"", 200)
	}
	messages := []session.Message{session.NewUserMessage("Please remember this verified workflow")}
	for i := range 150 {
		id := session.ToolCallID(fmt.Sprintf("call-%d", i))
		messages = append(messages,
			session.NewAssistantMessage("verified "+text, "", []session.ToolCall{{ID: id, Name: "Read", Args: json.RawMessage(`{}`)}}),
			session.NewToolMessage(session.ToolResult{CallID: id, Content: "verified " + strings.Repeat("y", 900)}),
		)
	}
	messages = append(messages, session.NewUserMessage("Please remember to run gofmt"))
	return automaticTrajectory("budget-source", messages, learning.MessageSpan{Start: len(messages) - 1, End: len(messages)})
}

func TestReflectionInputBudgetsRetainedMemory(t *testing.T) {
	// The coordinator retains Input, not just its much smaller wire projection.
	message := session.NewUserMessageWithParts("Please remember gofmt", []session.Content{{Data: make([]byte, defaultReflectionJobBytes+1)}})
	in := learning.Input{Trajectory: automaticTrajectory("retained-media", []session.Message{message}, learning.MessageSpan{})}
	projection, err := learning.ProjectInput(in)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) >= defaultReflectionJobBytes {
		t.Fatalf("fixture wire bytes=%d", len(wire))
	}
	if _, err := reflectionInputMaterial(in, defaultReflectionJobBytes); err == nil {
		t.Fatal("retained media escaped memory accounting")
	}
}

func TestReflectionInputBudgetsManualReselect(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		for _, escaped := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%s/escaped=%v", mode, escaped), func(t *testing.T) {
				o, r, calls := budgetTestObserver(t, agent.ReflectionLimits{}, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
				o.mode = mode
				tr := budgetTrajectory(escaped)
				host := []learning.Signal{{Kind: learning.SignalHostRequested}}
				initial, err := learning.MaterializeEvidence(t.Context(), learning.MaterializationRequest{Trajectory: tr, Signals: append(host, learning.DetectSignals(learning.Input{Trajectory: tr})...), Explicit: true, Limits: learning.MaterializationLimits{MaxBytes: defaultReflectionJobBytes}})
				if err != nil {
					t.Fatal(err)
				}
				first := initial.Input
				first.Trajectory.Workspace, first.Trajectory.Principal = tr.Workspace, tr.Principal.Clone()
				first.Trajectory.Kind, first.Trajectory.Counters = tr.Kind, tr.Counters
				first.Trajectory.Current = selectedCurrentSpan(initial.Manifest, tr.Current)
				first.Signals = append(host, learning.DetectSignals(first)...)
				if _, err := r.EvidenceReflector.RequestTokenEstimate(first); !errors.Is(err, agent.ErrReflectionLimits) {
					t.Fatalf("first exact production request error=%v", err)
				}
				projection, err := learning.ProjectInput(first)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := json.Marshal(projection)
				if err != nil {
					t.Fatal(err)
				}
				raw := reflectionRawInputBytes(first, 1<<20)
				if escaped && raw > defaultReflectionJobBytes {
					t.Fatal("escaped fixture should isolate serialized overhead")
				}
				if !escaped && raw <= defaultReflectionJobBytes {
					t.Fatal("plain fixture should exceed raw bound")
				}
				t.Logf("first candidate messages=%d raw=%d projection=%d", len(first.Trajectory.Messages), raw, len(wire))
				receipt, err := o.Reflect(t.Context(), tr, false)
				if err != nil {
					t.Fatal(err)
				}
				if *calls != 1 || !receipt.Abstained {
					t.Fatalf("calls=%d receipt=%+v", *calls, receipt)
				}
				in := r.input
				if len(in.Trajectory.Messages) >= len(first.Trajectory.Messages) {
					t.Fatal("evidence was not reselected")
				}
				if err := learning.ValidateInput(in); err != nil {
					t.Fatal(err)
				}
				if err := session.ValidateToolPairing(in.Trajectory.Messages); err != nil {
					t.Fatal(err)
				}
				if _, err := r.EvidenceReflector.RequestTokenEstimate(in); err != nil {
					t.Fatal(err)
				}
				tools := 0
				for _, entry := range in.Manifest.Entries {
					if entry.OriginalMessage == nil {
						continue
					}
					ordinal := *entry.OriginalMessage
					original, err := learning.MessageEvidenceRef(learning.Input{Trajectory: tr}, ordinal, "")
					if err != nil || original.Digest != entry.Digest {
						t.Fatalf("source digest changed for %d: %v", ordinal, err)
					}
					var local int
					if _, err := fmt.Sscanf(entry.Handle, "m:%d", &local); err != nil {
						t.Fatal(err)
					}
					want, got := tr.Messages[ordinal], in.Trajectory.Messages[local]
					if want.Text != got.Text {
						t.Fatalf("coordinate %d handle %s changed text", ordinal, entry.Handle)
					}
					if got.ToolResult != nil {
						tools++
						if want.ToolResult.CallID != got.ToolResult.CallID || want.ToolResult.Content != got.ToolResult.Content {
							t.Fatal("tool coordinate changed")
						}
					}
				}
				if tools == 0 {
					t.Fatal("fixture selected no tool components")
				}
			})
		}
	}
}

func TestReflectionInputBudgetsManualCollections(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		t.Run(mode.String(), func(t *testing.T) {
			o, r, calls := budgetTestObserver(t, agent.ReflectionLimits{}, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
			o.mode = mode
			memory := o.operatorMemory.(*memmemory.Store)
			for i := range 200 {
				rememberProfile(t.Context(), t, memory, tool.MemoryEntry{Key: fmt.Sprintf("user/fact_%03d", i), Value: strings.Repeat("<&>", 20)})
			}
			tr := automaticTrajectory("collections", []session.Message{session.NewUserMessage("Please remember gofmt")}, learning.MessageSpan{Start: 0, End: 1})
			source := func(yield func(session.Event, error) bool) {
				for i := range 400 {
					if !yield(session.Event{Type: session.EvResult, Seq: int64(i + 1), Result: &session.ResultPayload{Stop: session.StopEndTurn}}, nil) {
						return
					}
				}
			}
			receipt, err := o.reflectWithEventSource(t.Context(), tr, source)
			if err != nil {
				t.Fatal(err)
			}
			if *calls != 1 || !receipt.Abstained {
				t.Fatalf("calls=%d receipt=%+v", *calls, receipt)
			}
			if len(r.input.Existing) == 0 || len(r.input.Existing) >= 200 {
				t.Fatalf("facts=%d", len(r.input.Existing))
			}
			if len(r.input.Events) == 0 || len(r.input.Events) >= 400 {
				t.Fatalf("events=%d", len(r.input.Events))
			}
			if len(r.input.Trajectory.Messages) != 1 {
				t.Fatal("comparison facts starved evidence")
			}
			if _, err := r.EvidenceReflector.RequestTokenEstimate(r.input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReflectionInputBudgetsManualOptionalBytes(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		t.Run(mode.String(), func(t *testing.T) {
			o, r, calls := budgetTestObserver(t, agent.ReflectionLimits{InputBytes: 2048}, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
			o.mode = mode
			memory := o.operatorMemory.(*memmemory.Store)
			rememberProfile(t.Context(), t, memory, tool.MemoryEntry{Key: "user/a_small", Value: "concise"})
			rememberProfile(t.Context(), t, memory, tool.MemoryEntry{Key: "user/b_large", Value: strings.Repeat("<&>", 1000)})
			tr := automaticTrajectory("optional-bytes", []session.Message{session.NewUserMessage("Please remember gofmt")}, learning.MessageSpan{})
			if _, err := o.Reflect(t.Context(), tr, false); err != nil {
				t.Fatal(err)
			}
			if *calls != 1 || len(r.input.Existing) != 1 || len(r.input.Trajectory.Messages) != 1 {
				t.Fatalf("calls=%d facts=%d messages=%d", *calls, len(r.input.Existing), len(r.input.Trajectory.Messages))
			}
		})
	}
}

func TestReflectionInputBudgetsManualNoFit(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		t.Run(mode.String(), func(t *testing.T) {
			o, _, calls := budgetTestObserver(t, agent.ReflectionLimits{InputBytes: 1}, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
			o.mode = mode
			tr := durableAdmissionTrajectory("run_aaaaaaaaaaaaaaaaaaaaaaaaaa")
			// Off still executes manually even when a durable repository is configured.
			if mode == learning.Off {
				o.attempts = memattempt.New(wallclock.Clock{})
			}
			o.admission = newLearningAdmissionGate(1)
			receipt, err := o.Reflect(t.Context(), tr, false)
			if err != nil {
				t.Fatal(err)
			}
			if !receipt.Abstained || receipt.Disposition != reflectionCompleted || receipt.Err != learning.MaterializationNoEligibleEvidence.String() {
				t.Fatalf("receipt=%+v", receipt)
			}
			if *calls != 0 || o.admission.count != 0 {
				t.Fatalf("calls=%d cadence=%d", *calls, o.admission.count)
			}
			if o.attempts != nil {
				partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(tr.Principal))
				if err != nil {
					t.Fatal(err)
				}
				page, err := o.attempts.List(t.Context(), partition, learning.AttemptList{})
				if err != nil || len(page.Records) != 0 {
					t.Fatalf("attempts=%v err=%v", page.Records, err)
				}
			}
		})
	}
}

func TestReflectionInputBudgetsManualErrors(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		t.Run(mode.String(), func(t *testing.T) {
			o, _, calls := budgetTestObserver(t, agent.ReflectionLimits{}, mockllm.ErrorTurn(errors.New("offline provider failed")))
			o.mode = mode
			tr := automaticTrajectory("failure", []session.Message{session.NewUserMessage("Please remember gofmt")}, learning.MessageSpan{})
			if _, err := o.Reflect(t.Context(), tr, false); !errors.Is(err, agent.ErrReflectionProvider) {
				t.Fatalf("provider error=%v", err)
			}
			if *calls != 1 {
				t.Fatalf("calls=%d", *calls)
			}
			tr.SessionID = ""
			if _, err := o.Reflect(t.Context(), tr, false); !errors.Is(err, learning.ErrInvalidInput) {
				t.Fatalf("validation error=%v", err)
			}
			if *calls != 1 {
				t.Fatal("invalid input invoked provider")
			}
			sourceErr := errors.New("offline event source failed")
			tr.SessionID = "source-error"
			if _, err := o.reflectWithEventSource(t.Context(), tr, func(yield func(session.Event, error) bool) { yield(session.Event{}, sourceErr) }); !errors.Is(err, sourceErr) {
				t.Fatalf("source error=%v", err)
			}
		})
	}
}

func TestReflectionInputBudgetsLegacyAdmissionUnchanged(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, limit := range []int{0, 1} {
			t.Run(fmt.Sprintf("automatic=%v/limit=%d", automatic, limit), func(t *testing.T) {
				o, r, calls := budgetTestObserver(t, agent.ReflectionLimits{InputBytes: limit}, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
				o.mode = learning.Review
				tr := durableAdmissionTrajectory("run_aaaaaaaaaaaaaaaaaaaaaaaaaa")
				o.attempts = memattempt.New(wallclock.Clock{})
				o.sourceStore = persistedAdmissionSource(t, tr)
				o.admission = newLearningAdmissionGate(1)
				o.automatic = defaultLearningAutomaticConfig()
				o.ledger = automaticStoreForTest(t, t.TempDir(), o.automatic)
				receipt, err := o.submit(t.Context(), tr, nil, false, automatic)
				if automatic && limit == 1 {
					// Baseline returns the exact estimate's error without retrying.
					if !errors.Is(err, agent.ErrReflectionLimits) {
						t.Fatalf("error=%v", err)
					}
				} else if err != nil || receipt.Disposition != reflectionQueued {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
				// Only automatic admission estimates; durable explicit does not preflight.
				wantEstimates := 0
				if automatic {
					wantEstimates = 1
				}
				if r.estimates != wantEstimates {
					t.Fatalf("estimates=%d", r.estimates)
				}
				partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(tr.Principal))
				if err != nil {
					t.Fatal(err)
				}
				page, err := o.attempts.List(t.Context(), partition, learning.AttemptList{})
				if err != nil {
					t.Fatal(err)
				}
				wantAttempts, wantCadence := 1, 0
				if automatic {
					if limit == 1 {
						wantAttempts = 0
					} else {
						wantCadence = 1
					}
				}
				if len(page.Records) != wantAttempts || *calls != 0 || o.admission.count != wantCadence {
					t.Fatalf("attempts=%d calls=%d cadence=%d", len(page.Records), *calls, o.admission.count)
				}
			})
		}
	}
}
