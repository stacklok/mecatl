package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
)

type learnedSkillFailureRepository struct {
	learning.SkillRepository
	err   error
	calls int
}

func (r *learnedSkillFailureRepository) List(context.Context, learning.SkillPartition, learning.SkillList) (learning.SkillPage, error) {
	r.calls++
	return learning.SkillPage{}, r.err
}

type learnedSkillDiagnostic struct {
	level   port.Level
	message string
	fields  []any
}

type learnedSkillDiagnostics struct{ records []learnedSkillDiagnostic }

func (d *learnedSkillDiagnostics) Log(_ context.Context, level port.Level, message string, fields ...any) {
	d.records = append(d.records, learnedSkillDiagnostic{level, message, append([]any(nil), fields...)})
}

func (d *learnedSkillDiagnostics) With(...any) port.Diagnostics { return d }

func TestListLearnedSkillsFailureDiagnostics(t *testing.T) {
	for _, operation := range []string{"publish", "list"} {
		for _, tc := range []struct {
			name     string
			err      error
			category string
		}{
			{"generation", ErrLearnedSkillGenerationChanged, "generation_changed"},
			{"deadline", context.DeadlineExceeded, "deadline_exceeded"},
			{"canceled", context.Canceled, "canceled"},
			{"space", syscall.ENOSPC, "no_space"},
			{"quota", syscall.EDQUOT, "quota_exceeded"},
			{"unknown", errors.New("private skill body and credentials"), "unclassified"},
			{"text is not identity", errors.New(ErrLearnedSkillGenerationChanged.Error()), "unclassified"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				backendErr := fmt.Errorf("private skill body and credentials: %w", &os.PathError{Op: "read", Path: "/private/skill-store", Err: tc.err})
				repository := &learnedSkillFailureRepository{SkillRepository: memskill.New(), err: backendErr}
				diag := &learnedSkillDiagnostics{}
				publishCalls := 0
				svc := &Service{cfg: Config{
					LearnedSkills: repository,
					Diagnostics:   diag,
					PublishLearnedSkills: func(context.Context, learning.SkillPartition) error {
						publishCalls++
						if operation == "publish" {
							return backendErr
						}
						return nil
					},
				}}
				response, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{})
				if response != nil || !errors.Is(err, ErrInternal) || err.Error() != "server: internal error: learned skill operation failed" || errors.Is(err, backendErr) {
					t.Fatalf("client result = %v, %v; want unchanged generic internal error", response, err)
				}
				wireErr := statusForEntry(classifyError(err), err)
				if status.Code(wireErr) != codes.Internal || status.Convert(wireErr).Message() != err.Error() {
					t.Fatalf("wire error = %v", wireErr)
				}
				want := []learnedSkillDiagnostic{{port.LevelWarn, "learned-skill list failed", []any{"operation", operation, "category", tc.category}}}
				if !reflect.DeepEqual(diag.records, want) {
					t.Fatalf("diagnostics = %#v, want %#v", diag.records, want)
				}
				wantListCalls := 1
				if operation == "publish" {
					wantListCalls = 0
				}
				if publishCalls != 1 || repository.calls != wantListCalls {
					t.Fatalf("publish calls=%d list calls=%d; want 1 and %d (no retries)", publishCalls, repository.calls, wantListCalls)
				}
			})
		}
	}
}

func TestListLearnedSkillsDiagnosticsDoNotChangeOtherResults(t *testing.T) {
	diag := &learnedSkillDiagnostics{}
	svc := &Service{cfg: Config{LearnedSkills: memskill.New(), Diagnostics: diag}}
	if _, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{Limit: -1}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid limit: %v", err)
	}
	if len(diag.records) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diag.records)
	}
	svc.cfg.PublishLearnedSkills = func(context.Context, learning.SkillPartition) error { return learning.ErrSkillCursor }
	if _, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("known domain error mapping changed: %v", err)
	}
	svc.cfg.Diagnostics = nil
	svc.cfg.PublishLearnedSkills = func(context.Context, learning.SkillPartition) error { return syscall.ENOSPC }
	if _, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{}); !errors.Is(err, ErrInternal) {
		t.Fatalf("nil diagnostics changed error: %v", err)
	}
}
