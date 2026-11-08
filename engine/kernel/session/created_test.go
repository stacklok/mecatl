package session

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestHistoryScopedValuesValidateIdentity(t *testing.T) {
	for name, validate := range map[string]func() error{
		"created":      (Created{}).Validate,
		"instructions": (Instructions{}).Validate,
		"catalog":      (ToolCatalog{}).Validate,
		"binding":      (ModelBinding{}).Validate,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(); err == nil || err.Error() != "invalid session history ID" {
				t.Fatalf("invalid history identity error: %v", err)
			}
		})
	}
}

func TestCreated(t *testing.T) {
	v := Created{HistoryID: testHistoryID(t)}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := newCreatedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = codec.Decode[sessionformat.Created](r.format().Payload, testLimits())
	if err != nil || !v.Clone().Equal(v) {
		t.Fatalf("creation roundtrip: %v", err)
	}
	if (Created{}).Validate() == nil {
		t.Fatal("zero creation validated")
	}
	r, err = newCreatedRecord(Created{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || r != nil {
		t.Fatalf("zero creation record: %v", err)
	}
}
