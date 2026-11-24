package groupstorage_test

import (
	"errors"
	"testing"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestMemberPageRequestValidate(t *testing.T) {
	kind := resourceid.Kind("SES")
	after := resourceid.New(kind)
	for _, request := range []groupstorage.MemberPageRequest{
		{Limit: 1},
		{After: after, Limit: 1},
	} {
		if err := request.Validate(kind); err != nil {
			t.Errorf("Validate(%+v): %v", request, err)
		}
	}
	for _, tt := range []struct {
		name    string
		kind    resourceid.Kind
		request groupstorage.MemberPageRequest
	}{
		{"zero limit", kind, groupstorage.MemberPageRequest{}},
		{"negative limit", kind, groupstorage.MemberPageRequest{Limit: -1}},
		{"invalid kind", "ses", groupstorage.MemberPageRequest{Limit: 1}},
		{"wrong cursor kind", kind, groupstorage.MemberPageRequest{After: resourceid.New("GRP"), Limit: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.request.Validate(tt.kind); !errors.Is(err, history.ErrInvalid) {
				t.Fatalf("Validate() error = %v, want history.ErrInvalid", err)
			}
		})
	}
}
